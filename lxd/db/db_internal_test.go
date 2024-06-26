package db

import (
	"database/sql"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/lxc/lxd/lxd/db/node"
	"github.com/lxc/lxd/lxd/db/query"
	"github.com/lxc/lxd/lxd/types"
	"github.com/lxc/lxd/shared/api"
	"github.com/lxc/lxd/shared/logger"
	"github.com/lxc/lxd/shared/logging"
)

const DB_FIXTURES string = `
    INSERT INTO containers (name, architecture, type) VALUES ('thename', 1, 1);
    INSERT INTO profiles (name) VALUES ('theprofile');
    INSERT INTO containers_profiles (container_id, profile_id) VALUES (1, 3);
    INSERT INTO containers_config (container_id, key, value) VALUES (1, 'thekey', 'thevalue');
    INSERT INTO containers_devices (container_id, name, type) VALUES (1, 'somename', 1);
    INSERT INTO containers_devices_config (key, value, container_device_id) VALUES ('configkey', 'configvalue', 1);
    INSERT INTO images (fingerprint, filename, size, architecture, creation_date, expiry_date, upload_date) VALUES ('fingerprint', 'filename', 1024, 0,  1431547174,  1431547175,  1431547176);
    INSERT INTO images_aliases (name, image_id, description) VALUES ('somealias', 1, 'some description');
    INSERT INTO images_properties (image_id, type, key, value) VALUES (1, 0, 'thekey', 'some value');
    INSERT INTO profiles_config (profile_id, key, value) VALUES (3, 'thekey', 'thevalue');
    INSERT INTO profiles_devices (profile_id, name, type) VALUES (3, 'devicename', 1);
    INSERT INTO profiles_devices_config (profile_device_id, key, value) VALUES (3, 'devicekey', 'devicevalue');
    `

type dbTestSuite struct {
	suite.Suite

	dir string
	db  *Node
}

func (s *dbTestSuite) SetupTest() {
	s.db = s.CreateTestDb()
	_, err := s.db.DB().Exec(DB_FIXTURES)
	s.Nil(err)
}

func (s *dbTestSuite) TearDownTest() {
	s.db.DB().Close()
	os.RemoveAll(s.dir)
}

// Initialize a test in-memory DB.
func (s *dbTestSuite) CreateTestDb() *Node {
	var err error

	// Setup logging if main() hasn't been called/when testing
	if logger.Log == nil {
		logger.Log, err = logging.GetLogger("", "", true, true, nil)
		s.Nil(err)
	}

	s.dir, err = ioutil.TempDir("", "lxd-db-test")
	s.Nil(err)

	db, err := OpenNode(s.dir, nil, nil)
	s.Nil(err)

	return db

}

func TestDBTestSuite(t *testing.T) {
	suite.Run(t, new(dbTestSuite))
}

func (s *dbTestSuite) Test_deleting_a_container_cascades_on_related_tables() {
	var err error
	var count int
	var statements string

	// Drop the container we just created.
	statements = `DELETE FROM containers WHERE name = 'thename';`

	_, err = s.db.DB().Exec(statements)
	s.Nil(err, "Error deleting container!")

	// Make sure there are 0 container_profiles entries left.
	statements = `SELECT count(*) FROM containers_profiles;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a container didn't delete the profile association!")

	// Make sure there are 0 containers_config entries left.
	statements = `SELECT count(*) FROM containers_config;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a container didn't delete the associated container_config!")

	// Make sure there are 0 containers_devices entries left.
	statements = `SELECT count(*) FROM containers_devices;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a container didn't delete the associated container_devices!")

	// Make sure there are 0 containers_devices_config entries left.
	statements = `SELECT count(*) FROM containers_devices_config;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a container didn't delete the associated container_devices_config!")
}

func (s *dbTestSuite) Test_deleting_a_profile_cascades_on_related_tables() {
	var err error
	var count int
	var statements string

	// Drop the profile we just created.
	statements = `DELETE FROM profiles WHERE name = 'theprofile';`

	_, err = s.db.DB().Exec(statements)
	s.Nil(err)

	// Make sure there are 0 container_profiles entries left.
	statements = `SELECT count(*) FROM containers_profiles WHERE profile_id = 3;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a profile didn't delete the container association!")

	// Make sure there are 0 profiles_devices entries left.
	statements = `SELECT count(*) FROM profiles_devices WHERE profile_id == 3;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a profile didn't delete the related profiles_devices!")

	// Make sure there are 0 profiles_config entries left.
	statements = `SELECT count(*) FROM profiles_config WHERE profile_id == 3;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a profile didn't delete the related profiles_config! There are %d left")

	// Make sure there are 0 profiles_devices_config entries left.
	statements = `SELECT count(*) FROM profiles_devices_config WHERE profile_device_id == 4;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a profile didn't delete the related profiles_devices_config!")
}

func (s *dbTestSuite) Test_deleting_an_image_cascades_on_related_tables() {
	var err error
	var count int
	var statements string

	// Drop the image we just created.
	statements = `DELETE FROM images;`

	_, err = s.db.DB().Exec(statements)
	s.Nil(err)
	// Make sure there are 0 images_aliases entries left.
	statements = `SELECT count(*) FROM images_aliases;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting an image didn't delete the image alias association!")

	// Make sure there are 0 images_properties entries left.
	statements = `SELECT count(*) FROM images_properties;`
	err = s.db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting an image didn't delete the related images_properties!")
}

func (s *dbTestSuite) Test_running_UpdateFromV6_adds_on_delete_cascade() {
	// Upgrading the database schema with updateFromV6 adds ON DELETE CASCADE
	// to sqlite tables that require it, and conserve the data.

	var err error
	var count int

	db := s.CreateTestDb()
	defer db.DB().Close()

	statements := `
CREATE TABLE IF NOT EXISTS containers (
    id INTEGER primary key AUTOINCREMENT NOT NULL,
    name VARCHAR(255) NOT NULL,
    architecture INTEGER NOT NULL,
    type INTEGER NOT NULL,
    power_state INTEGER NOT NULL DEFAULT 0,
    ephemeral INTEGER NOT NULL DEFAULT 0,
    UNIQUE (name)
);
CREATE TABLE IF NOT EXISTS containers_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    container_id INTEGER NOT NULL,
    key VARCHAR(255) NOT NULL,
    value TEXT,
    FOREIGN KEY (container_id) REFERENCES containers (id),
    UNIQUE (container_id, key)
);

INSERT INTO containers (name, architecture, type) VALUES ('thename', 1, 1);
INSERT INTO containers_config (container_id, key, value) VALUES (1, 'thekey', 'thevalue');`

	_, err = db.DB().Exec(statements)
	s.Nil(err)

	// Run the upgrade from V6 code
	err = query.Transaction(db.DB(), node.UpdateFromV16)
	s.Nil(err)

	// Make sure the inserted data is still there.
	statements = `SELECT count(*) FROM containers_config;`
	err = db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 1, "There should be exactly one entry in containers_config!")

	// Drop the container.
	statements = `DELETE FROM containers WHERE name = 'thename';`

	_, err = db.DB().Exec(statements)
	s.Nil(err)

	// Make sure there are 0 container_profiles entries left.
	statements = `SELECT count(*) FROM containers_profiles;`
	err = db.DB().QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "Deleting a container didn't delete the profile association!")
}

func (s *dbTestSuite) Test_run_database_upgrades_with_some_foreign_keys_inconsistencies() {
	var db *sql.DB
	var err error
	var count int
	var statements string

	dir, err := ioutil.TempDir("", "lxd-db-test-")
	s.Nil(err)
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "lxd.db")
	db, err = sql.Open("sqlite3", path)
	defer db.Close()
	s.Nil(err)

	// This schema is a part of schema rev 1.
	statements = `
CREATE TABLE containers (
    id INTEGER primary key AUTOINCREMENT NOT NULL,
    name VARCHAR(255) NOT NULL,
    architecture INTEGER NOT NULL,
    type INTEGER NOT NULL,
    UNIQUE (name)
);
CREATE TABLE containers_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    container_id INTEGER NOT NULL,
    key VARCHAR(255) NOT NULL,
    value TEXT,
    FOREIGN KEY (container_id) REFERENCES containers (id),
    UNIQUE (container_id, key)
);
CREATE TABLE schema (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    version INTEGER NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (version)
);
CREATE TABLE images (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    fingerprint VARCHAR(255) NOT NULL,
    filename VARCHAR(255) NOT NULL,
    size INTEGER NOT NULL,
    public INTEGER NOT NULL DEFAULT 0,
    architecture INTEGER NOT NULL,
    creation_date DATETIME,
    expiry_date DATETIME,
    upload_date DATETIME NOT NULL,
    UNIQUE (fingerprint)
);
CREATE TABLE images_properties (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    image_id INTEGER NOT NULL,
    type INTEGER NOT NULL,
    key VARCHAR(255) NOT NULL,
    value TEXT,
    FOREIGN KEY (image_id) REFERENCES images (id)
);
CREATE TABLE certificates (
    id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
    fingerprint VARCHAR(255) NOT NULL,
    type INTEGER NOT NULL,
    name VARCHAR(255) NOT NULL,
    certificate TEXT NOT NULL,
    UNIQUE (fingerprint)
);
INSERT INTO schema (version, updated_at) values (1, "now");
INSERT INTO containers (name, architecture, type) VALUES ('thename', 1, 1);
INSERT INTO containers_config (container_id, key, value) VALUES (1, 'thekey', 'thevalue');`

	_, err = db.Exec(statements)
	s.Nil(err)

	// Now that we have a consistent schema, let's remove the container entry
	// *without* the ON DELETE CASCADE in place.
	statements = `DELETE FROM containers;`
	_, err = db.Exec(statements)
	s.Nil(err)

	// The "foreign key" on containers_config now points to nothing.
	// Let's run the schema upgrades.
	schema := node.Schema()
	_, err = schema.Ensure(db)
	s.Nil(err)

	// Make sure there are 0 containers_config entries left.
	statements = `SELECT count(*) FROM containers_config;`
	err = db.QueryRow(statements).Scan(&count)
	s.Nil(err)
	s.Equal(count, 0, "updateDb did not delete orphaned child entries after adding ON DELETE CASCADE!")
}

func (s *dbTestSuite) Test_ImageGet_finds_image_for_fingerprint() {
	var err error
	var result *api.Image

	_, result, err = s.db.ImageGet("fingerprint", false, false)
	s.Nil(err)
	s.NotNil(result)
	s.Equal(result.Filename, "filename")
	s.Equal(result.CreatedAt.UTC(), time.Unix(1431547174, 0).UTC())
	s.Equal(result.ExpiresAt.UTC(), time.Unix(1431547175, 0).UTC())
	s.Equal(result.UploadedAt.UTC(), time.Unix(1431547176, 0).UTC())
}

func (s *dbTestSuite) Test_ImageGet_for_missing_fingerprint() {
	var err error

	_, _, err = s.db.ImageGet("unknown", false, false)
	s.Equal(err, sql.ErrNoRows)
}

func (s *dbTestSuite) Test_ImageExists_true() {
	var err error

	exists, err := s.db.ImageExists("fingerprint")
	s.Nil(err)
	s.True(exists)
}

func (s *dbTestSuite) Test_ImageExists_false() {
	var err error

	exists, err := s.db.ImageExists("foobar")
	s.Nil(err)
	s.False(exists)
}

func (s *dbTestSuite) Test_ImageAliasGet_alias_exists() {
	var err error

	_, alias, err := s.db.ImageAliasGet("somealias", true)
	s.Nil(err)
	s.Equal(alias.Target, "fingerprint")
}

func (s *dbTestSuite) Test_ImageAliasGet_alias_does_not_exists() {
	var err error

	_, _, err = s.db.ImageAliasGet("whatever", true)
	s.Equal(err, NoSuchObjectError)
}

func (s *dbTestSuite) Test_ImageAliasAdd() {
	var err error

	err = s.db.ImageAliasAdd("Chaosphere", 1, "Someone will like the name")
	s.Nil(err)

	_, alias, err := s.db.ImageAliasGet("Chaosphere", true)
	s.Nil(err)
	s.Equal(alias.Target, "fingerprint")
}

func (s *dbTestSuite) Test_ContainerConfig() {
	var err error
	var result map[string]string
	var expected map[string]string

	_, err = s.db.DB().Exec("INSERT INTO containers_config (container_id, key, value) VALUES (1, 'something', 'something else');")
	s.Nil(err)

	result, err = s.db.ContainerConfig(1)
	s.Nil(err)

	expected = map[string]string{"thekey": "thevalue", "something": "something else"}

	for key, value := range expected {
		s.Equal(result[key], value,
			fmt.Sprintf("Mismatching value for key %s: %s != %s", key, result[key], value))
	}
}

func (s *dbTestSuite) Test_dbProfileConfig() {
	var err error
	var result map[string]string
	var expected map[string]string

	_, err = s.db.DB().Exec("INSERT INTO profiles_config (profile_id, key, value) VALUES (3, 'something', 'something else');")
	s.Nil(err)

	result, err = s.db.ProfileConfig("theprofile")
	s.Nil(err)

	expected = map[string]string{"thekey": "thevalue", "something": "something else"}

	for key, value := range expected {
		s.Equal(result[key], value,
			fmt.Sprintf("Mismatching value for key %s: %s != %s", key, result[key], value))
	}
}
func (s *dbTestSuite) Test_ContainerProfiles() {
	var err error
	var result []string
	var expected []string

	expected = []string{"theprofile"}
	result, err = s.db.ContainerProfiles(1)
	s.Nil(err)

	for i := range expected {
		s.Equal(expected[i], result[i],
			fmt.Sprintf("Mismatching contents for profile list: %s != %s", result[i], expected[i]))
	}
}

func (s *dbTestSuite) Test_dbDevices_profiles() {
	var err error
	var result types.Devices
	var subresult types.Device
	var expected types.Device

	result, err = s.db.Devices("theprofile", true)
	s.Nil(err)

	expected = types.Device{"type": "nic", "devicekey": "devicevalue"}
	subresult = result["devicename"]

	for key, value := range expected {
		s.Equal(subresult[key], value,
			fmt.Sprintf("Mismatching value for key %s: %v != %v", key, subresult[key], value))
	}
}

func (s *dbTestSuite) Test_dbDevices_containers() {
	var err error
	var result types.Devices
	var subresult types.Device
	var expected types.Device

	result, err = s.db.Devices("thename", false)
	s.Nil(err)

	expected = types.Device{"type": "nic", "configkey": "configvalue"}
	subresult = result["somename"]

	for key, value := range expected {
		s.Equal(subresult[key], value,
			fmt.Sprintf("Mismatching value for key %s: %s != %s", key, subresult[key], value))
	}
}
