package main

import (
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/state"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/logger"

	log "github.com/lxc/lxd/shared/log15"
)

var containersCmd = Command{
	name: "containers",
	get:  containersGet,
	post: containersPost,
}

var containerCmd = Command{
	name:   "containers/{name}",
	get:    containerGet,
	put:    containerPut,
	delete: containerDelete,
	post:   containerPost,
}

var containerStateCmd = Command{
	name: "containers/{name}/state",
	get:  containerState,
	put:  containerStatePut,
}

var containerFileCmd = Command{
	name: "containers/{name}/files",
	get:  containerFileHandler,
	post: containerFileHandler,
}

var containerSnapshotsCmd = Command{
	name: "containers/{name}/snapshots",
	get:  containerSnapshotsGet,
	post: containerSnapshotsPost,
}

var containerSnapshotCmd = Command{
	name:   "containers/{name}/snapshots/{snapshotName}",
	get:    snapshotHandler,
	post:   snapshotHandler,
	delete: snapshotHandler,
}

var containerExecCmd = Command{
	name: "containers/{name}/exec",
	post: containerExecPost,
}

type containerAutostartList []container

func (slice containerAutostartList) Len() int {
	return len(slice)
}

func (slice containerAutostartList) Less(i, j int) bool {
	iOrder := slice[i].ExpandedConfig()["boot.autostart.priority"]
	jOrder := slice[j].ExpandedConfig()["boot.autostart.priority"]

	if iOrder != jOrder {
		iOrderInt, _ := strconv.Atoi(iOrder)
		jOrderInt, _ := strconv.Atoi(jOrder)
		return iOrderInt > jOrderInt
	}

	return slice[i].Name() < slice[j].Name()
}

func (slice containerAutostartList) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}

func containersRestart(s *state.State, storage storage) error {
	// Get all the containers
	result, err := s.DB.ContainersList(db.CTypeRegular)
	if err != nil {
		return err
	}

	containers := []container{}

	for _, name := range result {
		c, err := containerLoadByName(s, storage, name)
		if err != nil {
			return err
		}

		containers = append(containers, c)
	}

	sort.Sort(containerAutostartList(containers))

	// Restart the containers
	for _, c := range containers {
		config := c.ExpandedConfig()
		lastState := config["volatile.last_state.power"]

		autoStart := config["boot.autostart"]
		autoStartDelay := config["boot.autostart.delay"]

		if shared.IsTrue(autoStart) || (autoStart == "" && lastState == "RUNNING") {
			if c.IsRunning() {
				continue
			}

			err = c.Start(false)
			if err != nil {
				logger.Errorf("Failed to start container '%s': %v", c.Name(), err)
			}

			autoStartDelayInt, err := strconv.Atoi(autoStartDelay)
			if err == nil {
				time.Sleep(time.Duration(autoStartDelayInt) * time.Second)
			}
		}
	}

	return nil
}

func containersShutdown(s *state.State, storage storage) error {
	var wg sync.WaitGroup

	// Get all the containers
	results, err := s.DB.ContainersList(db.CTypeRegular)
	if err != nil {
		return err
	}

	// Reset all container states
	err = s.DB.ContainersResetState()
	if err != nil {
		return err
	}

	for _, r := range results {
		// Load the container
		c, err := containerLoadByName(s, storage, r)
		if err != nil {
			return err
		}

		// Record the current state
		lastState := c.State()

		// Stop the container
		if lastState != "BROKEN" && lastState != "STOPPED" {
			wg.Add(1)
			go func(c container, lastState string) {
				c.Shutdown(time.Second * 30)
				c.Stop(false)
				c.ConfigKeySet("volatile.last_state.power", lastState)

				wg.Done()
			}(c, lastState)
		} else {
			c.ConfigKeySet("volatile.last_state.power", lastState)
		}
	}
	wg.Wait()

	return nil
}

func containerDeleteSnapshots(s *state.State, storage storage, cname string) error {
	logger.Debug("containerDeleteSnapshots",
		log.Ctx{"container": cname})

	results, err := s.DB.ContainerGetSnapshots(cname)
	if err != nil {
		return err
	}

	for _, sname := range results {
		sc, err := containerLoadByName(s, storage, sname)
		if err != nil {
			logger.Error(
				"containerDeleteSnapshots: Failed to load the snapshotcontainer",
				log.Ctx{"container": cname, "snapshot": sname})

			continue
		}

		if err := sc.Delete(); err != nil {
			logger.Error(
				"containerDeleteSnapshots: Failed to delete a snapshotcontainer",
				log.Ctx{"container": cname, "snapshot": sname, "err": err})
		}
	}

	return nil
}
