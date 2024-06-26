package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/state"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/api"
	"github.com/lxc/lxd/shared/logger"
	"github.com/lxc/lxd/shared/osarch"

	log "github.com/lxc/lxd/shared/log15"
)

/*
 * Update configuration, or, if 'restore:snapshot-name' is present, restore
 * the named snapshot
 */
func containerPut(d *Daemon, r *http.Request) Response {
	name := mux.Vars(r)["name"]
	c, err := containerLoadByName(d.State(), d.Storage, name)
	if err != nil {
		return NotFound
	}

	configRaw := api.ContainerPut{}
	if err := json.NewDecoder(r.Body).Decode(&configRaw); err != nil {
		return BadRequest(err)
	}

	architecture, err := osarch.ArchitectureId(configRaw.Architecture)
	if err != nil {
		architecture = 0
	}

	var do func(*operation) error
	if configRaw.Restore == "" {
		// Update container configuration
		do = func(op *operation) error {
			args := db.ContainerArgs{
				Architecture: architecture,
				Config:       configRaw.Config,
				Devices:      configRaw.Devices,
				Ephemeral:    configRaw.Ephemeral,
				Profiles:     configRaw.Profiles}

			// FIXME: should set to true when not migrating
			err = c.Update(args, false)
			if err != nil {
				return err
			}

			return nil
		}
	} else {
		// Snapshot Restore
		do = func(op *operation) error {
			return containerSnapRestore(d.State(), d.Storage, name, configRaw.Restore)
		}
	}

	resources := map[string][]string{}
	resources["containers"] = []string{name}

	op, err := operationCreate(operationClassTask, resources, nil, do, nil, nil)
	if err != nil {
		return InternalError(err)
	}

	return OperationResponse(op)
}

func containerSnapRestore(s *state.State, storage storage, name string, snap string) error {
	// normalize snapshot name
	if !shared.IsSnapshot(snap) {
		snap = name + shared.SnapshotDelimiter + snap
	}

	logger.Info(
		"RESTORE => Restoring snapshot",
		log.Ctx{
			"snapshot":  snap,
			"container": name})

	c, err := containerLoadByName(s, storage, name)
	if err != nil {
		logger.Error(
			"RESTORE => loadcontainerLXD() failed",
			log.Ctx{
				"container": name,
				"err":       err})
		return err
	}

	source, err := containerLoadByName(s, storage, snap)
	if err != nil {
		switch err {
		case sql.ErrNoRows:
			return fmt.Errorf("snapshot %s does not exist", snap)
		default:
			return err
		}
	}

	if err := c.Restore(source); err != nil {
		return err
	}

	return nil
}
