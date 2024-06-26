package main

import (
	"fmt"

	"github.com/gorilla/websocket"

	"github.com/lxc/lxd/lxd/migration"
	"github.com/lxc/lxd/shared/idmap"

	log "github.com/lxc/lxd/shared/log15"
)

type storageMock struct {
	d     *Daemon
	sType storageType
	log   log.Logger

	storageShared
}

func (s *storageMock) Init(config map[string]interface{}) (storage, error) {
	s.sType = storageTypeMock
	s.sTypeName = storageTypeToString(storageTypeMock)

	if err := s.initShared(); err != nil {
		return s, err
	}

	return s, nil
}

func (s *storageMock) GetStorageType() storageType {
	return s.sType
}

func (s *storageMock) GetStorageTypeName() string {
	return s.sTypeName
}

func (s *storageMock) ContainerCreate(container container) error {
	return nil
}

func (s *storageMock) ContainerCreateFromImage(
	container container, imageFingerprint string) error {

	return nil
}

func (s *storageMock) ContainerCanRestore(container container, sourceContainer container) error {
	return nil
}

func (s *storageMock) ContainerDelete(container container) error {
	return nil
}

func (s *storageMock) ContainerCopy(
	container container, sourceContainer container) error {

	return nil
}

func (s *storageMock) ContainerStart(name string, path string) error {
	return nil
}

func (s *storageMock) ContainerStop(name string, path string) error {
	return nil
}

func (s *storageMock) ContainerRename(
	container container, newName string) error {

	return nil
}

func (s *storageMock) ContainerRestore(
	container container, sourceContainer container) error {

	return nil
}

func (s *storageMock) ContainerSetQuota(
	container container, size int64) error {

	return nil
}

func (s *storageMock) ContainerGetUsage(
	container container) (int64, error) {

	return 0, nil
}
func (s *storageMock) ContainerSnapshotCreate(
	snapshotContainer container, sourceContainer container) error {

	return nil
}
func (s *storageMock) ContainerSnapshotDelete(
	snapshotContainer container) error {

	return nil
}

func (s *storageMock) ContainerSnapshotRename(
	snapshotContainer container, newName string) error {

	return nil
}

func (s *storageMock) ContainerSnapshotStart(container container) error {
	return nil
}

func (s *storageMock) ContainerSnapshotStop(container container) error {
	return nil
}

func (s *storageMock) ContainerSnapshotCreateEmpty(snapshotContainer container) error {
	return nil
}

func (s *storageMock) ImageCreate(fingerprint string) error {
	return nil
}

func (s *storageMock) ImageDelete(fingerprint string) error {
	return nil
}

func (s *storageMock) MigrationType() migration.MigrationFSType {
	return migration.MigrationFSType_RSYNC
}

func (s *storageMock) PreservesInodes() bool {
	return false
}

func (s *storageMock) MigrationSource(container container) (MigrationStorageSourceDriver, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *storageMock) MigrationSink(live bool, container container, snapshots []*migration.Snapshot, conn *websocket.Conn, srcIdmap *idmap.IdmapSet) error {
	return nil
}
