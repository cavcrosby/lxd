package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"

	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/migration"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/idmap"
	"github.com/lxc/lxd/shared/logger"

	log "github.com/lxc/lxd/shared/log15"
)

func storageLVMCheckVolumeGroup(vgName string) error {
	output, err := shared.RunCommand("vgdisplay", "-s", vgName)
	if err != nil {
		logger.Debug("vgdisplay failed to find vg", log.Ctx{"output": string(output)})
		return fmt.Errorf("LVM volume group '%s' not found", vgName)
	}

	return nil
}

func storageLVMThinpoolExists(vgName string, poolName string) (bool, error) {
	output, err := exec.Command("vgs", "--noheadings", "-o", "lv_attr", fmt.Sprintf("%s/%s", vgName, poolName)).Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			waitStatus := exitError.Sys().(syscall.WaitStatus)
			if waitStatus.ExitStatus() == 5 {
				// pool LV was not found
				return false, nil
			}
		}
		return false, fmt.Errorf("Error checking for pool '%s'", poolName)
	}
	// Found LV named poolname, check type:
	attrs := strings.TrimSpace(string(output[:]))
	if strings.HasPrefix(attrs, "t") {
		return true, nil
	}

	return false, fmt.Errorf("Pool named '%s' exists but is not a thin pool.", poolName)
}

func storageLVMGetThinPoolUsers(dbObj *db.Node) ([]string, error) {
	results := []string{}

	if daemonConfig["storage.lvm_vg_name"].Get() == "" {
		return results, nil
	}

	cNames, err := dbObj.ContainersList(db.CTypeRegular)
	if err != nil {
		return results, err
	}

	for _, cName := range cNames {
		var lvLinkPath string
		if strings.Contains(cName, shared.SnapshotDelimiter) {
			lvLinkPath = shared.VarPath("snapshots", fmt.Sprintf("%s.lv", cName))
		} else {
			lvLinkPath = shared.VarPath("containers", fmt.Sprintf("%s.lv", cName))
		}

		if shared.PathExists(lvLinkPath) {
			results = append(results, cName)
		}
	}

	imageNames, err := dbObj.ImagesGet(false)
	if err != nil {
		return results, err
	}

	for _, imageName := range imageNames {
		imageLinkPath := shared.VarPath("images", fmt.Sprintf("%s.lv", imageName))
		if shared.PathExists(imageLinkPath) {
			results = append(results, imageName)
		}
	}

	return results, nil
}

func storageLVMValidateThinPoolName(d *Daemon, key string, value string) error {
	return doStorageLVMValidateThinPoolName(d.db, key, value)
}

func doStorageLVMValidateThinPoolName(db *db.Node, key string, value string) error {
	users, err := storageLVMGetThinPoolUsers(db)
	if err != nil {
		return fmt.Errorf("Error checking if a pool is already in use: %v", err)
	}

	if len(users) > 0 {
		return fmt.Errorf("Can not change LVM config. Images or containers are still using LVs: %v", users)
	}

	vgname := daemonConfig["storage.lvm_vg_name"].Get()
	if value != "" {
		if vgname == "" {
			return fmt.Errorf("Can not set lvm_thinpool_name without lvm_vg_name set.")
		}

		poolExists, err := storageLVMThinpoolExists(vgname, value)
		if err != nil {
			return fmt.Errorf("Error checking for thin pool '%s' in '%s': %v", value, vgname, err)
		}

		if !poolExists {
			return fmt.Errorf("Pool '%s' does not exist in Volume Group '%s'", value, vgname)
		}
	}

	return nil
}

func storageLVMValidateVolumeGroupName(d *Daemon, key string, value string) error {
	users, err := storageLVMGetThinPoolUsers(d.db)
	if err != nil {
		return fmt.Errorf("Error checking if a pool is already in use: %v", err)
	}

	if len(users) > 0 {
		return fmt.Errorf("Can not change LVM config. Images or containers are still using LVs: %v", users)
	}

	if value != "" {
		err = storageLVMCheckVolumeGroup(value)
		if err != nil {
			return err
		}
	}

	return nil
}

func xfsGenerateNewUUID(lvpath string) error {
	output, err := shared.RunCommand(
		"xfs_admin",
		"-U", "generate",
		lvpath)
	if err != nil {
		return fmt.Errorf("Error generating new UUID: %v\noutput:'%s'", err, string(output))
	}

	return nil
}

func containerNameToLVName(containerName string) string {
	lvName := strings.Replace(containerName, "-", "--", -1)
	return strings.Replace(lvName, shared.SnapshotDelimiter, "-", -1)
}

type storageLvm struct {
	vgName string

	storageShared
}

func (s *storageLvm) Init(config map[string]interface{}) (storage, error) {
	s.sType = storageTypeLvm
	s.sTypeName = storageTypeToString(s.sType)
	if err := s.initShared(); err != nil {
		return s, err
	}

	output, err := shared.RunCommand("lvm", "version")
	if err != nil {
		return nil, fmt.Errorf("Error getting LVM version: %v\noutput:'%s'", err, string(output))
	}
	lines := strings.Split(string(output), "\n")

	s.sTypeVersion = ""
	for idx, line := range lines {
		fields := strings.SplitAfterN(line, ":", 2)
		if len(fields) < 2 {
			continue
		}
		if idx > 0 {
			s.sTypeVersion += " / "
		}
		s.sTypeVersion += strings.TrimSpace(fields[1])
	}

	if config["vgName"] == nil {
		vgName := daemonConfig["storage.lvm_vg_name"].Get()
		if vgName == "" {
			return s, fmt.Errorf("LVM isn't enabled")
		}

		if err := storageLVMCheckVolumeGroup(vgName); err != nil {
			return s, err
		}
		s.vgName = vgName
	} else {
		s.vgName = config["vgName"].(string)
	}

	return s, nil
}

func versionSplit(versionString string) (int, int, int, error) {
	fs := strings.Split(versionString, ".")
	majs, mins, incs := fs[0], fs[1], fs[2]

	maj, err := strconv.Atoi(majs)
	if err != nil {
		return 0, 0, 0, err
	}
	min, err := strconv.Atoi(mins)
	if err != nil {
		return 0, 0, 0, err
	}
	incs = strings.Split(incs, "(")[0]
	inc, err := strconv.Atoi(incs)
	if err != nil {
		return 0, 0, 0, err
	}

	return maj, min, inc, nil
}

func (s *storageLvm) lvmVersionIsAtLeast(versionString string) (bool, error) {
	lvmVersion := strings.Split(s.sTypeVersion, "/")[0]

	lvmMaj, lvmMin, lvmInc, err := versionSplit(lvmVersion)
	if err != nil {
		return false, err
	}

	inMaj, inMin, inInc, err := versionSplit(versionString)
	if err != nil {
		return false, err
	}

	if lvmMaj < inMaj || lvmMin < inMin || lvmInc < inInc {
		return false, nil
	} else {
		return true, nil
	}

}

func (s *storageLvm) ContainerCreate(container container) error {
	containerName := containerNameToLVName(container.Name())
	lvpath, err := s.createThinLV(containerName)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(container.Path(), 0755); err != nil {
		return err
	}

	var mode os.FileMode
	if container.IsPrivileged() {
		mode = 0700
	} else {
		mode = 0755
	}

	err = os.Chmod(container.Path(), mode)
	if err != nil {
		return err
	}

	dst := fmt.Sprintf("%s.lv", container.Path())
	err = os.Symlink(lvpath, dst)
	if err != nil {
		return err
	}

	return nil
}

func (s *storageLvm) ContainerCreateFromImage(
	container container, imageFingerprint string) error {

	imageLVFilename := shared.VarPath(
		"images", fmt.Sprintf("%s.lv", imageFingerprint))

	if !shared.PathExists(imageLVFilename) {
		if err := s.ImageCreate(imageFingerprint); err != nil {
			return err
		}
	}

	containerName := containerNameToLVName(container.Name())

	lvpath, err := s.createSnapshotLV(containerName, imageFingerprint, false)
	if err != nil {
		return err
	}

	destPath := container.Path()
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return fmt.Errorf("Error creating container directory: %v", err)
	}

	err = os.Chmod(destPath, 0700)
	if err != nil {
		return err
	}

	dst := shared.VarPath("containers", fmt.Sprintf("%s.lv", container.Name()))
	err = os.Symlink(lvpath, dst)
	if err != nil {
		return err
	}

	// Generate a new xfs's UUID
	fstype := daemonConfig["storage.lvm_fstype"].Get()
	if fstype == "xfs" {
		err := xfsGenerateNewUUID(lvpath)
		if err != nil {
			s.ContainerDelete(container)
			return err
		}
	}

	err = tryMount(lvpath, destPath, fstype, 0, "discard")
	if err != nil {
		s.ContainerDelete(container)
		return fmt.Errorf("Error mounting snapshot LV: %v", err)
	}

	var mode os.FileMode
	if container.IsPrivileged() {
		mode = 0700
	} else {
		mode = 0755
	}

	err = os.Chmod(destPath, mode)
	if err != nil {
		return err
	}

	if !container.IsPrivileged() {
		if err = s.shiftRootfs(container); err != nil {
			err2 := tryUnmount(destPath, 0)
			if err2 != nil {
				return fmt.Errorf("Error in umount: '%s' while cleaning up after error in shiftRootfs: '%s'", err2, err)
			}
			s.ContainerDelete(container)
			return fmt.Errorf("Error in shiftRootfs: %v", err)
		}
	}

	err = container.TemplateApply("create")
	if err != nil {
		s.log.Error("Error in create template during ContainerCreateFromImage, continuing to unmount",
			log.Ctx{"err": err})
	}

	umounterr := tryUnmount(destPath, 0)
	if umounterr != nil {
		return fmt.Errorf("Error unmounting '%s' after shiftRootfs: %v", destPath, umounterr)
	}

	return err
}

func (s *storageLvm) ContainerCanRestore(container container, sourceContainer container) error {
	return nil
}

func (s *storageLvm) ContainerDelete(container container) error {
	lvName := containerNameToLVName(container.Name())
	if err := s.removeLV(lvName); err != nil {
		return err
	}

	lvLinkPath := fmt.Sprintf("%s.lv", container.Path())
	if err := os.Remove(lvLinkPath); err != nil {
		return err
	}

	cPath := container.Path()
	if err := os.RemoveAll(cPath); err != nil {
		s.log.Error("ContainerDelete: failed to remove path", log.Ctx{"cPath": cPath, "err": err})
		return fmt.Errorf("Cleaning up %s: %s", cPath, err)
	}

	return nil
}

func (s *storageLvm) ContainerCopy(container container, sourceContainer container) error {
	if s.isLVMContainer(sourceContainer) {
		if err := s.createSnapshotContainer(container, sourceContainer, false); err != nil {
			s.log.Error("Error creating snapshot LV for copy", log.Ctx{"err": err})
			return err
		}
	} else {
		s.log.Info("Copy from Non-LVM container", log.Ctx{"container": container.Name(),
			"sourceContainer": sourceContainer.Name()})
		if err := s.ContainerCreate(container); err != nil {
			s.log.Error("Error creating empty container", log.Ctx{"err": err})
			return err
		}

		if err := s.ContainerStart(container.Name(), container.Path()); err != nil {
			s.log.Error("Error starting/mounting container", log.Ctx{"err": err, "container": container.Name()})
			s.ContainerDelete(container)
			return err
		}

		output, err := storageRsyncCopy(
			sourceContainer.Path(),
			container.Path())
		if err != nil {
			s.log.Error("ContainerCopy: rsync failed", log.Ctx{"output": string(output)})
			s.ContainerDelete(container)
			return fmt.Errorf("rsync failed: %s", string(output))
		}

		if err := s.ContainerStop(container.Name(), container.Path()); err != nil {
			return err
		}
	}
	return container.TemplateApply("copy")
}

func (s *storageLvm) ContainerStart(name string, path string) error {
	lvName := containerNameToLVName(name)
	lvpath := fmt.Sprintf("/dev/%s/%s", s.vgName, lvName)
	fstype := daemonConfig["storage.lvm_fstype"].Get()

	err := tryMount(lvpath, path, fstype, 0, "discard")
	if err != nil {
		return fmt.Errorf(
			"Error mounting snapshot LV path='%s': %v",
			path,
			err)
	}

	return nil
}

func (s *storageLvm) ContainerStop(name string, path string) error {
	err := tryUnmount(path, 0)
	if err != nil {
		return fmt.Errorf(
			"failed to unmount container path '%s'.\nError: %v",
			path,
			err)
	}

	return nil
}

func (s *storageLvm) ContainerRename(
	container container, newContainerName string) error {

	oldName := containerNameToLVName(container.Name())
	newName := containerNameToLVName(newContainerName)
	output, err := s.renameLV(oldName, newName)
	if err != nil {
		s.log.Error("Failed to rename a container LV",
			log.Ctx{"oldName": oldName,
				"newName": newName,
				"err":     err,
				"output":  string(output)})

		return fmt.Errorf("Failed to rename a container LV, oldName='%s', newName='%s', err='%s'", oldName, newName, err)
	}

	// Rename the snapshots
	if !container.IsSnapshot() {
		snaps, err := container.Snapshots()
		if err != nil {
			return err
		}

		for _, snap := range snaps {
			baseSnapName := filepath.Base(snap.Name())
			newSnapshotName := newName + shared.SnapshotDelimiter + baseSnapName
			err := s.ContainerRename(snap, newSnapshotName)
			if err != nil {
				return err
			}

			oldPathParent := filepath.Dir(snap.Path())
			if ok, _ := shared.PathIsEmpty(oldPathParent); ok {
				os.Remove(oldPathParent)
			}
		}
	}

	// Create a new symlink
	newSymPath := fmt.Sprintf("%s.lv", containerPath(newContainerName, container.IsSnapshot()))

	err = os.MkdirAll(filepath.Dir(containerPath(newContainerName, container.IsSnapshot())), 0700)
	if err != nil {
		return err
	}

	err = os.Symlink(fmt.Sprintf("/dev/%s/%s", s.vgName, newName), newSymPath)
	if err != nil {
		return err
	}

	// Remove the old symlink
	oldSymPath := fmt.Sprintf("%s.lv", container.Path())
	err = os.Remove(oldSymPath)
	if err != nil {
		return err
	}

	// Rename the directory
	err = os.Rename(container.Path(), containerPath(newContainerName, container.IsSnapshot()))
	if err != nil {
		return err
	}

	return nil

}

func (s *storageLvm) ContainerRestore(
	container container, sourceContainer container) error {
	srcName := containerNameToLVName(sourceContainer.Name())
	destName := containerNameToLVName(container.Name())

	err := s.removeLV(destName)
	if err != nil {
		return fmt.Errorf("Error removing LV about to be restored over: %v", err)
	}

	_, err = s.createSnapshotLV(destName, srcName, false)
	if err != nil {
		return fmt.Errorf("Error creating snapshot LV: %v", err)
	}

	return nil
}

func (s *storageLvm) ContainerSetQuota(container container, size int64) error {
	return fmt.Errorf("The LVM container backend doesn't support quotas.")
}

func (s *storageLvm) ContainerGetUsage(container container) (int64, error) {
	return -1, fmt.Errorf("The LVM container backend doesn't support quotas.")
}

func (s *storageLvm) ContainerSnapshotCreate(
	snapshotContainer container, sourceContainer container) error {
	return s.createSnapshotContainer(snapshotContainer, sourceContainer, true)
}

func (s *storageLvm) createSnapshotContainer(
	snapshotContainer container, sourceContainer container, readonly bool) error {

	srcName := containerNameToLVName(sourceContainer.Name())
	destName := containerNameToLVName(snapshotContainer.Name())
	logger.Debug(
		"Creating snapshot",
		log.Ctx{"srcName": srcName, "destName": destName})

	lvpath, err := s.createSnapshotLV(destName, srcName, readonly)
	if err != nil {
		return fmt.Errorf("Error creating snapshot LV: %v", err)
	}

	destPath := snapshotContainer.Path()
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return fmt.Errorf("Error creating container directory: %v", err)
	}

	var mode os.FileMode
	if snapshotContainer.IsPrivileged() {
		mode = 0700
	} else {
		mode = 0755
	}

	err = os.Chmod(destPath, mode)
	if err != nil {
		return err
	}

	dest := fmt.Sprintf("%s.lv", snapshotContainer.Path())
	err = os.Symlink(lvpath, dest)
	if err != nil {
		return err
	}

	return nil
}

func (s *storageLvm) ContainerSnapshotDelete(
	snapshotContainer container) error {

	err := s.ContainerDelete(snapshotContainer)
	if err != nil {
		return fmt.Errorf("Error deleting snapshot %s: %s", snapshotContainer.Name(), err)
	}

	oldPathParent := filepath.Dir(snapshotContainer.Path())
	if ok, _ := shared.PathIsEmpty(oldPathParent); ok {
		os.Remove(oldPathParent)
	}
	return nil
}

func (s *storageLvm) ContainerSnapshotRename(
	snapshotContainer container, newContainerName string) error {
	oldName := containerNameToLVName(snapshotContainer.Name())
	newName := containerNameToLVName(newContainerName)
	oldPath := snapshotContainer.Path()
	oldSymPath := fmt.Sprintf("%s.lv", oldPath)
	newPath := containerPath(newContainerName, true)
	newSymPath := fmt.Sprintf("%s.lv", newPath)

	// Rename the LV
	output, err := s.renameLV(oldName, newName)
	if err != nil {
		s.log.Error("Failed to rename a snapshot LV",
			log.Ctx{"oldName": oldName, "newName": newName, "err": err, "output": string(output)})
		return fmt.Errorf("Failed to rename a container LV, oldName='%s', newName='%s', err='%s'", oldName, newName, err)
	}

	// Delete the symlink
	err = os.Remove(oldSymPath)
	if err != nil {
		return fmt.Errorf("Failed to remove old symlink: %s", err)
	}

	// Create the symlink
	err = os.Symlink(fmt.Sprintf("/dev/%s/%s", s.vgName, newName), newSymPath)
	if err != nil {
		return fmt.Errorf("Failed to create symlink: %s", err)
	}

	// Rename the mount point
	err = os.Rename(oldPath, newPath)
	if err != nil {
		return fmt.Errorf("Failed to rename mountpoint: %s", err)
	}

	return nil
}

func (s *storageLvm) ContainerSnapshotStart(container container) error {
	srcName := containerNameToLVName(container.Name())
	destName := containerNameToLVName(container.Name() + "/rw")

	logger.Debug(
		"Creating snapshot",
		log.Ctx{"srcName": srcName, "destName": destName})

	lvpath, err := s.createSnapshotLV(destName, srcName, false)
	if err != nil {
		return fmt.Errorf("Error creating snapshot LV: %v", err)
	}

	destPath := container.Path()
	if !shared.PathExists(destPath) {
		if err := os.MkdirAll(destPath, 0755); err != nil {
			return fmt.Errorf("Error creating container directory: %v", err)
		}
	}

	// Generate a new xfs's UUID
	fstype := daemonConfig["storage.lvm_fstype"].Get()
	if fstype == "xfs" {
		err := xfsGenerateNewUUID(lvpath)
		if err != nil {
			s.ContainerDelete(container)
			return err
		}
	}

	err = tryMount(lvpath, container.Path(), fstype, 0, "discard")
	if err != nil {
		return fmt.Errorf(
			"Error mounting snapshot LV path='%s': %v",
			container.Path(),
			err)
	}

	return nil
}

func (s *storageLvm) ContainerSnapshotStop(container container) error {
	err := s.ContainerStop(container.Name(), container.Path())
	if err != nil {
		return err
	}

	lvName := containerNameToLVName(container.Name() + "/rw")
	if err := s.removeLV(lvName); err != nil {
		return err
	}

	return nil
}

func (s *storageLvm) ContainerSnapshotCreateEmpty(snapshotContainer container) error {
	return s.ContainerCreate(snapshotContainer)
}

func (s *storageLvm) ImageCreate(fingerprint string) error {
	finalName := shared.VarPath("images", fingerprint)

	lvpath, err := s.createThinLV(fingerprint)
	if err != nil {
		s.log.Error("LVMCreateThinLV", log.Ctx{"err": err})
		return fmt.Errorf("Error Creating LVM LV for new image: %v", err)
	}

	dst := shared.VarPath("images", fmt.Sprintf("%s.lv", fingerprint))
	err = os.Symlink(lvpath, dst)
	if err != nil {
		return err
	}

	tempLVMountPoint, err := ioutil.TempDir(shared.VarPath("images"), "tmp_lv_mnt")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(tempLVMountPoint); err != nil {
			s.log.Error("Deleting temporary LVM mount point", log.Ctx{"err": err})
		}
	}()

	fstype := daemonConfig["storage.lvm_fstype"].Get()
	err = tryMount(lvpath, tempLVMountPoint, fstype, 0, "discard")
	if err != nil {
		logger.Infof("Error mounting image LV for unpacking: %v", err)
		return fmt.Errorf("Error mounting image LV: %v", err)
	}

	unpackErr := unpackImage(finalName, tempLVMountPoint, s.storage.GetStorageType(), s.s.OS.RunningInUserNS)

	err = tryUnmount(tempLVMountPoint, 0)
	if err != nil {
		s.log.Warn("could not unmount LV. Will not remove",
			log.Ctx{"lvpath": lvpath, "mountpoint": tempLVMountPoint, "err": err})
		if unpackErr == nil {
			return err
		}

		return fmt.Errorf(
			"Error unmounting '%s' during cleanup of error %v",
			tempLVMountPoint, unpackErr)
	}

	if unpackErr != nil {
		s.removeLV(fingerprint)
		return unpackErr
	}

	return nil
}

func (s *storageLvm) ImageDelete(fingerprint string) error {
	err := s.removeLV(fingerprint)
	if err != nil {
		return err
	}

	lvsymlink := fmt.Sprintf(
		"%s.lv", shared.VarPath("images", fingerprint))
	err = os.Remove(lvsymlink)
	if err != nil {
		return fmt.Errorf(
			"Failed to remove symlink to deleted image LV: '%s': %v", lvsymlink, err)
	}

	return nil
}

func (s *storageLvm) createDefaultThinPool() (string, error) {
	thinPoolName := daemonConfig["storage.lvm_thinpool_name"].Get()
	isRecent, err := s.lvmVersionIsAtLeast("2.02.99")
	if err != nil {
		return "", fmt.Errorf("Error checking LVM version: %v", err)
	}

	// Create the thin pool
	var output string
	if isRecent {
		output, err = shared.TryRunCommand(
			"lvcreate",
			"--poolmetadatasize", "1G",
			"-l", "100%FREE",
			"--thinpool",
			fmt.Sprintf("%s/%s", s.vgName, thinPoolName))
	} else {
		output, err = shared.TryRunCommand(
			"lvcreate",
			"--poolmetadatasize", "1G",
			"-L", "1G",
			"--thinpool",
			fmt.Sprintf("%s/%s", s.vgName, thinPoolName))
	}

	if err != nil {
		s.log.Error(
			"Could not create thin pool",
			log.Ctx{
				"name":   thinPoolName,
				"err":    err,
				"output": string(output)})

		return "", fmt.Errorf(
			"Could not create LVM thin pool named %s", thinPoolName)
	}

	if !isRecent {
		// Grow it to the maximum VG size (two step process required by old LVM)
		output, err = shared.TryRunCommand(
			"lvextend",
			"--alloc", "anywhere",
			"-l", "100%FREE",
			fmt.Sprintf("%s/%s", s.vgName, thinPoolName))

		if err != nil {
			s.log.Error(
				"Could not grow thin pool",
				log.Ctx{
					"name":   thinPoolName,
					"err":    err,
					"output": string(output)})

			return "", fmt.Errorf(
				"Could not grow LVM thin pool named %s", thinPoolName)
		}
	}

	return thinPoolName, nil
}

func (s *storageLvm) createThinLV(lvname string) (string, error) {
	var err error

	vgname := daemonConfig["storage.lvm_vg_name"].Get()
	poolname := daemonConfig["storage.lvm_thinpool_name"].Get()
	exists, err := storageLVMThinpoolExists(vgname, poolname)
	if err != nil {
		return "", err
	}

	if !exists {
		poolname, err = s.createDefaultThinPool()
		if err != nil {
			return "", fmt.Errorf("Error creating LVM thin pool: %v", err)
		}

		err = doStorageLVMValidateThinPoolName(s.s.DB, "", poolname)
		if err != nil {
			s.log.Error("Setting thin pool name", log.Ctx{"err": err})
			return "", fmt.Errorf("Error setting LVM thin pool config: %v", err)
		}
	}

	lvSize := daemonConfig["storage.lvm_volume_size"].Get()

	output, err := shared.TryRunCommand(
		"lvcreate",
		"--thin",
		"-n", lvname,
		"--virtualsize", lvSize,
		fmt.Sprintf("%s/%s", s.vgName, poolname))
	if err != nil {
		s.log.Error("Could not create LV", log.Ctx{"lvname": lvname, "output": string(output)})
		return "", fmt.Errorf("Could not create thin LV named %s", lvname)
	}

	lvpath := fmt.Sprintf("/dev/%s/%s", s.vgName, lvname)

	fstype := daemonConfig["storage.lvm_fstype"].Get()
	switch fstype {
	case "xfs":
		output, err = shared.TryRunCommand(
			"mkfs.xfs",
			lvpath)
	default:
		// default = ext4
		output, err = shared.TryRunCommand(
			"mkfs.ext4",
			"-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0",
			lvpath)
	}

	if err != nil {
		s.log.Error("Filesystem creation failed", log.Ctx{"output": string(output)})
		return "", fmt.Errorf("Error making filesystem on image LV: %v", err)
	}

	return lvpath, nil
}

func (s *storageLvm) removeLV(lvname string) error {
	var err error
	var output string

	output, err = shared.TryRunCommand(
		"lvremove", "-f", fmt.Sprintf("%s/%s", s.vgName, lvname))

	if err != nil {
		s.log.Error("Could not remove LV", log.Ctx{"lvname": lvname, "output": string(output)})
		return fmt.Errorf("Could not remove LV named %s", lvname)
	}

	return nil
}

func (s *storageLvm) createSnapshotLV(lvname string, origlvname string, readonly bool) (string, error) {
	s.log.Debug("in createSnapshotLV:", log.Ctx{"lvname": lvname, "dev string": fmt.Sprintf("/dev/%s/%s", s.vgName, origlvname)})
	isRecent, err := s.lvmVersionIsAtLeast("2.02.99")
	if err != nil {
		return "", fmt.Errorf("Error checking LVM version: %v", err)
	}

	var output string
	if isRecent {
		output, err = shared.TryRunCommand(
			"lvcreate",
			"-kn",
			"-n", lvname,
			"-s", fmt.Sprintf("/dev/%s/%s", s.vgName, origlvname))
	} else {
		output, err = shared.TryRunCommand(
			"lvcreate",
			"-n", lvname,
			"-s", fmt.Sprintf("/dev/%s/%s", s.vgName, origlvname))
	}
	if err != nil {
		s.log.Error("Could not create LV snapshot", log.Ctx{"lvname": lvname, "origlvname": origlvname, "output": string(output)})
		return "", fmt.Errorf("Could not create snapshot LV named %s", lvname)
	}

	snapshotFullName := fmt.Sprintf("/dev/%s/%s", s.vgName, lvname)

	if readonly {
		output, err = shared.TryRunCommand("lvchange", "-ay", "-pr", snapshotFullName)
	} else {
		output, err = shared.TryRunCommand("lvchange", "-ay", snapshotFullName)
	}

	if err != nil {
		return "", fmt.Errorf("Could not activate new snapshot '%s': %v\noutput:%s", lvname, err, string(output))
	}

	return snapshotFullName, nil
}

func (s *storageLvm) isLVMContainer(container container) bool {
	return shared.PathExists(fmt.Sprintf("%s.lv", container.Path()))
}

func (s *storageLvm) renameLV(oldName string, newName string) (string, error) {
	output, err := shared.TryRunCommand("lvrename", s.vgName, oldName, newName)
	return string(output), err
}

func (s *storageLvm) MigrationType() migration.MigrationFSType {
	return migration.MigrationFSType_RSYNC
}

func (s *storageLvm) PreservesInodes() bool {
	return false
}

func (s *storageLvm) MigrationSource(container container) (MigrationStorageSourceDriver, error) {
	return rsyncMigrationSource(container)
}

func (s *storageLvm) MigrationSink(live bool, container container, snapshots []*migration.Snapshot, conn *websocket.Conn, srcIdmap *idmap.IdmapSet) error {
	return rsyncMigrationSink(live, container, snapshots, conn, srcIdmap)
}
