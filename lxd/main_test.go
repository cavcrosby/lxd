package main

import (
	"io/ioutil"
	"os"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/idmap"
)

func mockStartDaemon() (*Daemon, error) {
	d := DefaultDaemon()
	d.os.MockMode = true

	// Setup test certificates. We re-use the ones already on disk under
	// the test/ directory, to avoid generating new ones, which is
	// expensive.
	err := setupTestCerts(shared.VarPath())
	if err != nil {
		return nil, err
	}

	if err := d.Init(); err != nil {
		return nil, err
	}

	d.os.IdmapSet = &idmap.IdmapSet{Idmap: []idmap.IdmapEntry{
		{Isuid: true, Hostid: 100000, Nsid: 0, Maprange: 500000},
		{Isgid: true, Hostid: 100000, Nsid: 0, Maprange: 500000},
	}}

	// Call this after Init so we have a log object.
	storageConfig := make(map[string]interface{})
	d.Storage = &storageLogWrapper{w: &storageMock{d: d}}
	if _, err := d.Storage.Init(storageConfig); err != nil {
		return nil, err
	}

	return d, nil
}

type lxdTestSuite struct {
	suite.Suite
	d      *Daemon
	Req    *require.Assertions
	tmpdir string
}

func (suite *lxdTestSuite) SetupTest() {
	tmpdir, err := ioutil.TempDir("", "lxd_testrun_")
	if err != nil {
		suite.T().Fatalf("failed to create temp dir: %v", err)
	}
	suite.tmpdir = tmpdir

	if err := os.Setenv("LXD_DIR", suite.tmpdir); err != nil {
		suite.T().Fatalf("failed to set LXD_DIR: %v", err)
	}

	suite.d, err = mockStartDaemon()
	if err != nil {
		suite.T().Fatalf("failed to start daemon: %v", err)
	}
	suite.Req = require.New(suite.T())

	daemonConfigInit(suite.d.db.DB())
	suite.Req = require.New(suite.T())
}

func (suite *lxdTestSuite) TearDownTest() {
	suite.d.Stop()
	err := os.RemoveAll(suite.tmpdir)
	if err != nil {
		suite.T().Fatalf("failed to remove temp dir: %v", err)
	}
}
