package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/mattn/go-sqlite3"

	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/state"
	"github.com/lxc/lxd/lxd/util"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/logger"

	log "github.com/lxc/lxd/shared/log15"
)

var deviceSchedRebalance = make(chan []string, 2)

type deviceBlockLimit struct {
	readBps   int64
	readIops  int64
	writeBps  int64
	writeIops int64
}

type deviceTaskCPU struct {
	id    int
	strId string
	count *int
}
type deviceTaskCPUs []deviceTaskCPU

func (c deviceTaskCPUs) Len() int           { return len(c) }
func (c deviceTaskCPUs) Less(i, j int) bool { return *c[i].count < *c[j].count }
func (c deviceTaskCPUs) Swap(i, j int)      { c[i], c[j] = c[j], c[i] }

func deviceNetlinkListener() (chan []string, chan []string, error) {
	NETLINK_KOBJECT_UEVENT := 15
	UEVENT_BUFFER_SIZE := 2048

	fd, err := syscall.Socket(
		syscall.AF_NETLINK, syscall.SOCK_RAW,
		NETLINK_KOBJECT_UEVENT,
	)

	if err != nil {
		return nil, nil, err
	}

	nl := syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
		Pid:    uint32(os.Getpid()),
		Groups: 1,
	}

	err = syscall.Bind(fd, &nl)
	if err != nil {
		return nil, nil, err
	}

	chCPU := make(chan []string, 1)
	chNetwork := make(chan []string, 0)

	go func(chCPU chan []string, chNetwork chan []string) {
		b := make([]byte, UEVENT_BUFFER_SIZE*2)
		for {
			_, err := syscall.Read(fd, b)
			if err != nil {
				continue
			}

			props := map[string]string{}
			last := 0
			for i, e := range b {
				if i == len(b) || e == 0 {
					msg := string(b[last+1 : i])
					last = i
					if len(msg) == 0 || msg == "\x00" {
						continue
					}

					fields := strings.SplitN(msg, "=", 2)
					if len(fields) != 2 {
						continue
					}

					props[fields[0]] = fields[1]
				}
			}

			if props["SUBSYSTEM"] == "cpu" {
				if props["DRIVER"] != "processor" {
					continue
				}

				if props["ACTION"] != "offline" && props["ACTION"] != "online" {
					continue
				}

				// As CPU re-balancing affects all containers, no need to queue them
				select {
				case chCPU <- []string{path.Base(props["DEVPATH"]), props["ACTION"]}:
				default:
					// Channel is full, drop the event
				}
			}

			if props["SUBSYSTEM"] == "net" {
				if props["ACTION"] != "add" && props["ACTION"] != "removed" {
					continue
				}

				if !shared.PathExists(fmt.Sprintf("/sys/class/net/%s", props["INTERFACE"])) {
					continue
				}

				// Network balancing is interface specific, so queue everything
				chNetwork <- []string{props["INTERFACE"], props["ACTION"]}
			}
		}
	}(chCPU, chNetwork)

	return chCPU, chNetwork, nil
}

func parseCpuset(cpu string) ([]int, error) {
	cpus := []int{}
	chunks := strings.Split(cpu, ",")
	for _, chunk := range chunks {
		if strings.Contains(chunk, "-") {
			// Range
			fields := strings.SplitN(chunk, "-", 2)
			if len(fields) != 2 {
				return nil, fmt.Errorf("Invalid cpuset value: %s", cpu)
			}

			low, err := strconv.Atoi(fields[0])
			if err != nil {
				return nil, fmt.Errorf("Invalid cpuset value: %s", cpu)
			}

			high, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("Invalid cpuset value: %s", cpu)
			}

			for i := low; i <= high; i++ {
				cpus = append(cpus, i)
			}
		} else {
			// Simple entry
			nr, err := strconv.Atoi(chunk)
			if err != nil {
				return nil, fmt.Errorf("Invalid cpuset value: %s", cpu)
			}
			cpus = append(cpus, nr)
		}
	}
	return cpus, nil
}

func deviceTaskBalance(s *state.State, storage storage) {
	min := func(x, y int) int {
		if x < y {
			return x
		}
		return y
	}

	// Don't bother running when CGroup support isn't there
	if !s.OS.CGroupCPUsetController {
		return
	}

	// Get effective cpus list - those are all guaranteed to be online
	effectiveCpus, err := cGroupGet("cpuset", "/", "cpuset.effective_cpus")
	if err != nil {
		// Older kernel - use cpuset.cpus
		effectiveCpus, err = cGroupGet("cpuset", "/", "cpuset.cpus")
		if err != nil {
			logger.Errorf("Error reading host's cpuset.cpus")
			return
		}
	}

	effectiveCpusInt, err := parseCpuset(effectiveCpus)
	if err != nil {
		logger.Errorf("Error parsing effective CPU set")
		return
	}

	isolatedCpusInt := []int{}
	if shared.PathExists("/sys/devices/system/cpu/isolated") {
		buf, err := ioutil.ReadFile("/sys/devices/system/cpu/isolated")
		if err != nil {
			logger.Errorf("Error reading host's isolated cpu")
			return
		}

		// File might exist even though there are no isolated cpus.
		isolatedCpus := strings.TrimSpace(string(buf))
		if isolatedCpus != "" {
			isolatedCpusInt, err = parseCpuset(isolatedCpus)
			if err != nil {
				logger.Errorf("Error parsing isolated CPU set: %s", string(isolatedCpus))
				return
			}
		}
	}

	effectiveCpusSlice := []string{}
	for _, id := range effectiveCpusInt {
		if shared.IntInSlice(id, isolatedCpusInt) {
			continue
		}

		effectiveCpusSlice = append(effectiveCpusSlice, fmt.Sprintf("%d", id))
	}

	effectiveCpus = strings.Join(effectiveCpusSlice, ",")

	err = cGroupSet("cpuset", "/lxc", "cpuset.cpus", effectiveCpus)
	if err != nil && shared.PathExists("/sys/fs/cgroup/cpuset/lxc") {
		logger.Warn("Error setting lxd's cpuset.cpus", log.Ctx{"err": err})
	}
	cpus, err := parseCpuset(effectiveCpus)
	if err != nil {
		logger.Error("Error parsing host's cpu set", log.Ctx{"cpuset": effectiveCpus, "err": err})
		return
	}

	// Iterate through the containers
	containers, err := s.DB.ContainersList(db.CTypeRegular)
	if err != nil {
		logger.Error("problem loading containers list", log.Ctx{"err": err})
		return
	}
	fixedContainers := map[int][]container{}
	balancedContainers := map[container]int{}
	for _, name := range containers {
		c, err := containerLoadByName(s, storage, name)
		if err != nil {
			continue
		}

		conf := c.ExpandedConfig()
		cpulimit, ok := conf["limits.cpu"]
		if !ok || cpulimit == "" {
			cpulimit = effectiveCpus
		}

		if !c.IsRunning() {
			continue
		}

		count, err := strconv.Atoi(cpulimit)
		if err == nil {
			// Load-balance
			count = min(count, len(cpus))
			balancedContainers[c] = count
		} else {
			// Pinned
			containerCpus, err := parseCpuset(cpulimit)
			if err != nil {
				return
			}
			for _, nr := range containerCpus {
				if !shared.IntInSlice(nr, cpus) {
					continue
				}

				_, ok := fixedContainers[nr]
				if ok {
					fixedContainers[nr] = append(fixedContainers[nr], c)
				} else {
					fixedContainers[nr] = []container{c}
				}
			}
		}
	}

	// Balance things
	pinning := map[container][]string{}
	usage := map[int]deviceTaskCPU{}

	for _, id := range cpus {
		cpu := deviceTaskCPU{}
		cpu.id = id
		cpu.strId = fmt.Sprintf("%d", id)
		count := 0
		cpu.count = &count

		usage[id] = cpu
	}

	for cpu, ctns := range fixedContainers {
		c, ok := usage[cpu]
		if !ok {
			logger.Errorf("Internal error: container using unavailable cpu")
			continue
		}
		id := c.strId
		for _, ctn := range ctns {
			_, ok := pinning[ctn]
			if ok {
				pinning[ctn] = append(pinning[ctn], id)
			} else {
				pinning[ctn] = []string{id}
			}
			*c.count += 1
		}
	}

	sortedUsage := make(deviceTaskCPUs, 0)
	for _, value := range usage {
		sortedUsage = append(sortedUsage, value)
	}

	for ctn, count := range balancedContainers {
		sort.Sort(sortedUsage)
		for _, cpu := range sortedUsage {
			if count == 0 {
				break
			}
			count -= 1

			id := cpu.strId
			_, ok := pinning[ctn]
			if ok {
				pinning[ctn] = append(pinning[ctn], id)
			} else {
				pinning[ctn] = []string{id}
			}
			*cpu.count += 1
		}
	}

	// Set the new pinning
	for ctn, set := range pinning {
		// Confirm the container didn't just stop
		if !ctn.IsRunning() {
			continue
		}

		sort.Strings(set)
		err := ctn.CGroupSet("cpuset.cpus", strings.Join(set, ","))
		if err != nil {
			logger.Error("balance: Unable to set cpuset", log.Ctx{"name": ctn.Name(), "err": err, "value": strings.Join(set, ",")})
		}
	}
}

func deviceNetworkPriority(s *state.State, storage storage, netif string) {
	// Don't bother running when CGroup support isn't there
	if !s.OS.CGroupNetPrioController {
		return
	}

	containers, err := s.DB.ContainersList(db.CTypeRegular)
	if err != nil {
		return
	}

	for _, name := range containers {
		// Get the container struct
		c, err := containerLoadByName(s, storage, name)
		if err != nil {
			continue
		}

		// Extract the current priority
		networkPriority := c.ExpandedConfig()["limits.network.priority"]
		if networkPriority == "" {
			continue
		}

		networkInt, err := strconv.Atoi(networkPriority)
		if err != nil {
			continue
		}

		// Set the value for the new interface
		c.CGroupSet("net_prio.ifpriomap", fmt.Sprintf("%s %d", netif, networkInt))
	}

	return
}

func deviceEventListener(s *state.State, storage storage) {
	chNetlinkCPU, chNetlinkNetwork, err := deviceNetlinkListener()
	if err != nil {
		logger.Errorf("scheduler: couldn't setup netlink listener")
		return
	}

	for {
		select {
		case e := <-chNetlinkCPU:
			if len(e) != 2 {
				logger.Errorf("Scheduler: received an invalid cpu hotplug event")
				continue
			}

			if !s.OS.CGroupCPUsetController {
				continue
			}

			logger.Debugf("Scheduler: cpu: %s is now %s: re-balancing", e[0], e[1])
			deviceTaskBalance(s, storage)
		case e := <-chNetlinkNetwork:
			if len(e) != 2 {
				logger.Errorf("Scheduler: received an invalid network hotplug event")
				continue
			}

			if !s.OS.CGroupNetPrioController {
				continue
			}

			logger.Debugf("Scheduler: network: %s has been added: updating network priorities", e[0])
			deviceNetworkPriority(s, storage, e[0])
		case e := <-deviceSchedRebalance:
			if len(e) != 3 {
				logger.Errorf("Scheduler: received an invalid rebalance event")
				continue
			}

			if !s.OS.CGroupCPUsetController {
				continue
			}

			logger.Debugf("Scheduler: %s %s %s: re-balancing", e[0], e[1], e[2])
			deviceTaskBalance(s, storage)
		}
	}
}

func deviceTaskSchedulerTrigger(srcType string, srcName string, srcStatus string) {
	// Spawn a go routine which then triggers the scheduler
	select {
	case deviceSchedRebalance <- []string{srcType, srcName, srcStatus}:
	default:
		// Channel is full, drop the event
	}
}

func deviceIsBlockdev(path string) bool {
	// Get a stat struct from the provided path
	stat := syscall.Stat_t{}
	err := syscall.Stat(path, &stat)
	if err != nil {
		return false
	}

	// Check if it's a block device
	if stat.Mode&syscall.S_IFMT == syscall.S_IFBLK {
		return true
	}

	// Not a device
	return false
}

func deviceModeOct(strmode string) (int, error) {
	// Default mode
	if strmode == "" {
		return 0600, nil
	}

	// Converted mode
	i, err := strconv.ParseInt(strmode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("Bad device mode: %s", strmode)
	}

	return int(i), nil
}

func deviceGetAttributes(path string) (string, int, int, error) {
	// Get a stat struct from the provided path
	stat := syscall.Stat_t{}
	err := syscall.Stat(path, &stat)
	if err != nil {
		return "", 0, 0, err
	}

	// Check what kind of file it is
	dType := ""
	if stat.Mode&syscall.S_IFMT == syscall.S_IFBLK {
		dType = "b"
	} else if stat.Mode&syscall.S_IFMT == syscall.S_IFCHR {
		dType = "c"
	} else {
		return "", 0, 0, fmt.Errorf("Not a device")
	}

	// Return the device information
	major := shared.Major(stat.Rdev)
	minor := shared.Minor(stat.Rdev)
	return dType, major, minor, nil
}

func deviceNextInterfaceHWAddr() (string, error) {
	// Generate a new random MAC address using the usual prefix
	ret := bytes.Buffer{}
	for _, c := range "00:16:3e:xx:xx:xx" {
		if c == 'x' {
			c, err := rand.Int(rand.Reader, big.NewInt(16))
			if err != nil {
				return "", err
			}
			ret.WriteString(fmt.Sprintf("%x", c.Int64()))
		} else {
			ret.WriteString(string(c))
		}
	}

	return ret.String(), nil
}

func deviceNextVeth() string {
	// Return a new random veth device name
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	return "veth" + hex.EncodeToString(randBytes)
}

func deviceRemoveInterface(nic string) error {
	_, err := shared.RunCommand("ip", "link", "del", "dev", nic)
	return err
}

func deviceMountDisk(srcPath string, dstPath string, readonly bool, recursive bool) error {
	var err error

	// Prepare the mount flags
	flags := 0
	if readonly {
		flags |= syscall.MS_RDONLY
	}

	// Detect the filesystem
	fstype := "none"
	if deviceIsBlockdev(srcPath) {
		fstype, err = shared.BlockFsDetect(srcPath)
		if err != nil {
			return err
		}
	} else {
		flags |= syscall.MS_BIND
		if recursive {
			flags |= syscall.MS_REC
		}
	}

	// Mount the filesystem
	if err = syscall.Mount(srcPath, dstPath, fstype, uintptr(flags), ""); err != nil {
		return fmt.Errorf("Unable to mount %s at %s: %s", srcPath, dstPath, err)
	}

	// Remount bind mounts in readonly mode if requested
	if readonly == true && flags&syscall.MS_BIND == syscall.MS_BIND {
		flags = syscall.MS_RDONLY | syscall.MS_BIND | syscall.MS_REMOUNT
		if err = syscall.Mount("", dstPath, fstype, uintptr(flags), ""); err != nil {
			return fmt.Errorf("Unable to mount %s in readonly mode: %s", dstPath, err)
		}
	}

	flags = syscall.MS_REC | syscall.MS_SLAVE
	if err = syscall.Mount("", dstPath, "", uintptr(flags), ""); err != nil {
		return fmt.Errorf("unable to make mount %s private: %s", dstPath, err)
	}

	return nil
}

func deviceParseCPU(cpuAllowance string, cpuPriority string) (string, string, string, error) {
	var err error

	// Parse priority
	cpuShares := 0
	cpuPriorityInt := 10
	if cpuPriority != "" {
		cpuPriorityInt, err = strconv.Atoi(cpuPriority)
		if err != nil {
			return "", "", "", err
		}
	}
	cpuShares -= 10 - cpuPriorityInt

	// Parse allowance
	cpuCfsQuota := "-1"
	cpuCfsPeriod := "100000"

	if cpuAllowance != "" {
		if strings.HasSuffix(cpuAllowance, "%") {
			// Percentage based allocation
			percent, err := strconv.Atoi(strings.TrimSuffix(cpuAllowance, "%"))
			if err != nil {
				return "", "", "", err
			}

			cpuShares += (10 * percent) + 24
		} else {
			// Time based allocation
			fields := strings.SplitN(cpuAllowance, "/", 2)
			if len(fields) != 2 {
				return "", "", "", fmt.Errorf("Invalid allowance: %s", cpuAllowance)
			}

			quota, err := strconv.Atoi(strings.TrimSuffix(fields[0], "ms"))
			if err != nil {
				return "", "", "", err
			}

			period, err := strconv.Atoi(strings.TrimSuffix(fields[1], "ms"))
			if err != nil {
				return "", "", "", err
			}

			// Set limit in ms
			cpuCfsQuota = fmt.Sprintf("%d", quota*1000)
			cpuCfsPeriod = fmt.Sprintf("%d", period*1000)
			cpuShares += 1024
		}
	} else {
		// Default is 100%
		cpuShares += 1024
	}

	// Deal with a potential negative score
	if cpuShares < 0 {
		cpuShares = 0
	}

	return fmt.Sprintf("%d", cpuShares), cpuCfsQuota, cpuCfsPeriod, nil
}

func deviceGetParentBlocks(path string) ([]string, error) {
	var devices []string
	var device []string

	// Expand the mount path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	expPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		expPath = absPath
	}

	// Find the source mount of the path
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	match := ""
	for scanner.Scan() {
		line := scanner.Text()
		rows := strings.Fields(line)

		if len(rows[4]) <= len(match) {
			continue
		}

		if expPath != rows[4] && !strings.HasPrefix(expPath, rows[4]) {
			continue
		}

		match = rows[4]

		// Go backward to avoid problems with optional fields
		device = []string{rows[2], rows[len(rows)-2]}
	}

	if device == nil {
		return nil, fmt.Errorf("Couldn't find a match /proc/self/mountinfo entry")
	}

	// Handle the most simple case
	if !strings.HasPrefix(device[0], "0:") {
		return []string{device[0]}, nil
	}

	// Deal with per-filesystem oddities. We don't care about failures here
	// because any non-special filesystem => directory backend.
	fs, _ := util.FilesystemDetect(expPath)

	if fs == "zfs" && shared.PathExists("/dev/zfs") {
		// Accessible zfs filesystems
		poolName := strings.Split(device[1], "/")[0]

		output, err := shared.RunCommand("zpool", "status", "-P", "-L", poolName)
		if err != nil {
			return nil, fmt.Errorf("Failed to query zfs filesystem information for %s: %s", device[1], output)
		}

		header := true
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}

			if fields[1] != "ONLINE" {
				continue
			}

			if header {
				header = false
				continue
			}

			var path string
			if shared.PathExists(fields[0]) {
				if shared.IsBlockdevPath(fields[0]) {
					path = fields[0]
				} else {
					subDevices, err := deviceGetParentBlocks(fields[0])
					if err != nil {
						return nil, err
					}

					for _, dev := range subDevices {
						devices = append(devices, dev)
					}
				}
			} else {
				continue
			}

			if path != "" {
				_, major, minor, err := deviceGetAttributes(path)
				if err != nil {
					continue
				}

				devices = append(devices, fmt.Sprintf("%d:%d", major, minor))
			}
		}

		if len(devices) == 0 {
			return nil, fmt.Errorf("Unable to find backing block for zfs pool: %s", poolName)
		}
	} else if fs == "btrfs" && shared.PathExists(device[1]) {
		// Accessible btrfs filesystems
		output, err := shared.RunCommand("btrfs", "filesystem", "show", device[1])
		if err != nil {
			return nil, fmt.Errorf("Failed to query btrfs filesystem information for %s: %s", device[1], output)
		}

		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != "devid" {
				continue
			}

			_, major, minor, err := deviceGetAttributes(fields[len(fields)-1])
			if err != nil {
				return nil, err
			}

			devices = append(devices, fmt.Sprintf("%d:%d", major, minor))
		}
	} else if shared.PathExists(device[1]) {
		// Anything else with a valid path
		_, major, minor, err := deviceGetAttributes(device[1])
		if err != nil {
			return nil, err
		}

		devices = append(devices, fmt.Sprintf("%d:%d", major, minor))
	} else {
		return nil, fmt.Errorf("Invalid block device: %s", device[1])
	}

	return devices, nil
}

func deviceParseDiskLimit(readSpeed string, writeSpeed string) (int64, int64, int64, int64, error) {
	parseValue := func(value string) (int64, int64, error) {
		var err error

		bps := int64(0)
		iops := int64(0)

		if readSpeed == "" {
			return bps, iops, nil
		}

		if strings.HasSuffix(value, "iops") {
			iops, err = strconv.ParseInt(strings.TrimSuffix(value, "iops"), 10, 64)
			if err != nil {
				return -1, -1, err
			}
		} else {
			bps, err = shared.ParseByteSizeString(value)
			if err != nil {
				return -1, -1, err
			}
		}

		return bps, iops, nil
	}

	readBps, readIops, err := parseValue(readSpeed)
	if err != nil {
		return -1, -1, -1, -1, err
	}

	writeBps, writeIops, err := parseValue(writeSpeed)
	if err != nil {
		return -1, -1, -1, -1, err
	}

	return readBps, readIops, writeBps, writeIops, nil
}
