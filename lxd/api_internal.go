package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/lxc/lxd/shared/logger"

	log "github.com/lxc/lxd/shared/log15"
)

var apiInternal = []Command{
	internalReadyCmd,
	internalShutdownCmd,
	internalContainerOnStartCmd,
	internalContainerOnStopCmd,
}

func internalReady(d *Daemon, r *http.Request) Response {
	if !d.config.SetupMode {
		return InternalError(fmt.Errorf("The server isn't currently in setup mode"))
	}

	err := d.Ready()
	if err != nil {
		return InternalError(err)
	}

	d.config.SetupMode = false

	return EmptySyncResponse
}

func internalWaitReady(d *Daemon, r *http.Request) Response {
	<-d.readyChan

	return EmptySyncResponse
}

func internalShutdown(d *Daemon, r *http.Request) Response {
	d.shutdownChan <- true

	return EmptySyncResponse
}

func internalContainerOnStart(d *Daemon, r *http.Request) Response {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		return SmartError(err)
	}

	c, err := containerLoadById(d.State(), d.Storage, id)
	if err != nil {
		return SmartError(err)
	}

	err = c.OnStart()
	if err != nil {
		logger.Error("start hook failed", log.Ctx{"container": c.Name(), "err": err})
		return SmartError(err)
	}

	return EmptySyncResponse
}

func internalContainerOnStop(d *Daemon, r *http.Request) Response {
	id, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		return SmartError(err)
	}

	target := r.FormValue("target")
	if target == "" {
		target = "unknown"
	}

	c, err := containerLoadById(d.State(), d.Storage, id)
	if err != nil {
		return SmartError(err)
	}

	err = c.OnStop(target)
	if err != nil {
		logger.Error("stop hook failed", log.Ctx{"container": c.Name(), "err": err})
		return SmartError(err)
	}

	return EmptySyncResponse
}

var internalShutdownCmd = Command{name: "shutdown", put: internalShutdown}
var internalReadyCmd = Command{name: "ready", put: internalReady, get: internalWaitReady}
var internalContainerOnStartCmd = Command{name: "containers/{id}/onstart", get: internalContainerOnStart}
var internalContainerOnStopCmd = Command{name: "containers/{id}/onstop", get: internalContainerOnStop}
