package shared

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lxc/lxd/shared/cancel"
	"github.com/lxc/lxd/shared/ioprogress"
)

const SnapshotDelimiter = "/"
const DefaultPort = "8443"

// URLEncode encodes a path and query parameters to a URL.
func URLEncode(path string, query map[string]string) (string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return "", err
	}

	params := url.Values{}
	for key, value := range query {
		params.Add(key, value)
	}
	u.RawQuery = params.Encode()
	return u.String(), nil
}

// AddSlash adds a slash to the end of paths if they don't already have one.
// This can be useful for rsyncing things, since rsync has behavior present on
// the presence or absence of a trailing slash.
func AddSlash(path string) string {
	if path[len(path)-1] != '/' {
		return path + "/"
	}

	return path
}

func PathExists(name string) bool {
	_, err := os.Lstat(name)
	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}

// PathIsEmpty checks if the given path is empty.
func PathIsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// read in ONLY one file
	_, err = f.Readdir(1)

	// and if the file is EOF... well, the dir is empty.
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

// IsDir returns true if the given path is a directory.
func IsDir(name string) bool {
	stat, err := os.Lstat(name)
	if err != nil {
		return false
	}
	return stat.IsDir()
}

// IsUnixSocket returns true if the given path is either a Unix socket
// or a symbolic link pointing at a Unix socket.
func IsUnixSocket(path string) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeSocket) == os.ModeSocket
}

// HostPath returns the host path for the provided path
// On a normal system, this does nothing
// When inside of a snap environment, returns the real path
func HostPath(path string) string {
	// Ignore empty paths
	if len(path) == 0 {
		return path
	}

	// Don't prefix stdin/stdout
	if path == "-" {
		return path
	}

	// Check if we're running in a snap package
	snap := os.Getenv("SNAP")
	snapName := os.Getenv("SNAP_NAME")
	if snap == "" || snapName != "lxd" {
		return path
	}

	// Handle relative paths
	if path[0] != os.PathSeparator {
		// Use the cwd of the parent as snap-confine alters our own cwd on launch
		ppid := os.Getppid()
		if ppid < 1 {
			return path
		}

		pwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", ppid))
		if err != nil {
			return path
		}

		path = filepath.Clean(strings.Join([]string{pwd, path}, string(os.PathSeparator)))
	}

	// Check if the path is already snap-aware
	for _, prefix := range []string{"/dev", "/snap", "/var/snap", "/var/lib/snapd"} {
		if path == prefix || strings.HasPrefix(path, fmt.Sprintf("%s/", prefix)) {
			return path
		}
	}

	return fmt.Sprintf("/var/lib/snapd/hostfs%s", path)
}

// VarPath returns the provided path elements joined by a slash and
// appended to the end of $LXD_DIR, which defaults to /var/lib/lxd.
func VarPath(path ...string) string {
	varDir := os.Getenv("LXD_DIR")
	if varDir == "" {
		varDir = "/var/lib/lxd"
	}

	items := []string{varDir}
	items = append(items, path...)
	return filepath.Join(items...)
}

// CachePath returns the directory that LXD should its cache under. If LXD_DIR is
// set, this path is $LXD_DIR/cache, otherwise it is /var/cache/lxd.
func CachePath(path ...string) string {
	varDir := os.Getenv("LXD_DIR")
	logDir := "/var/cache/lxd"
	if varDir != "" {
		logDir = filepath.Join(varDir, "cache")
	}
	items := []string{logDir}
	items = append(items, path...)
	return filepath.Join(items...)
}

// LogPath returns the directory that LXD should put logs under. If LXD_DIR is
// set, this path is $LXD_DIR/logs, otherwise it is /var/log/lxd.
func LogPath(path ...string) string {
	varDir := os.Getenv("LXD_DIR")
	logDir := "/var/log/lxd"
	if varDir != "" {
		logDir = filepath.Join(varDir, "logs")
	}
	items := []string{logDir}
	items = append(items, path...)
	return filepath.Join(items...)
}

func ParseLXDFileHeaders(headers http.Header) (uid int64, gid int64, mode int, type_ string, write string) {
	uid, err := strconv.ParseInt(headers.Get("X-LXD-uid"), 10, 64)
	if err != nil {
		uid = -1
	}

	gid, err = strconv.ParseInt(headers.Get("X-LXD-gid"), 10, 64)
	if err != nil {
		gid = -1
	}

	mode, err = strconv.Atoi(headers.Get("X-LXD-mode"))
	if err != nil {
		mode = -1
	} else {
		rawMode, err := strconv.ParseInt(headers.Get("X-LXD-mode"), 0, 0)
		if err == nil {
			mode = int(os.FileMode(rawMode) & os.ModePerm)
		}
	}

	type_ = headers.Get("X-LXD-type")
	/* backwards compat: before "type" was introduced, we could only
	 * manipulate files
	 */
	if type_ == "" {
		type_ = "file"
	}

	write = headers.Get("X-LXD-write")
	/* backwards compat: before "write" was introduced, we could only
	 * overwrite files
	 */
	if write == "" {
		write = "overwrite"
	}

	return uid, gid, mode, type_, write
}

func ReadToJSON(r io.Reader, req interface{}) error {
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}

	return json.Unmarshal(buf, req)
}

func ReaderToChannel(r io.Reader, bufferSize int) <-chan []byte {
	if bufferSize <= 128*1024 {
		bufferSize = 128 * 1024
	}

	ch := make(chan ([]byte))

	go func() {
		readSize := 128 * 1024
		offset := 0
		buf := make([]byte, bufferSize)

		for {
			read := buf[offset : offset+readSize]
			nr, err := r.Read(read)
			offset += nr
			if offset > 0 && (offset+readSize >= bufferSize || err != nil) {
				ch <- buf[0:offset]
				offset = 0
				buf = make([]byte, bufferSize)
			}

			if err != nil {
				close(ch)
				break
			}
		}
	}()

	return ch
}

// Returns a random base64 encoded string from crypto/rand.
func RandomCryptoString() (string, error) {
	buf := make([]byte, 32)
	n, err := rand.Read(buf)
	if err != nil {
		return "", err
	}

	if n != len(buf) {
		return "", fmt.Errorf("not enough random bytes read")
	}

	return hex.EncodeToString(buf), nil
}

func SplitExt(fpath string) (string, string) {
	b := path.Base(fpath)
	ext := path.Ext(fpath)
	return b[:len(b)-len(ext)], ext
}

func AtoiEmptyDefault(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}

	return strconv.Atoi(s)
}

func ReadStdin() ([]byte, error) {
	buf := bufio.NewReader(os.Stdin)
	line, _, err := buf.ReadLine()
	if err != nil {
		return nil, err
	}
	return line, nil
}

func WriteAll(w io.Writer, data []byte) error {
	buf := bytes.NewBuffer(data)

	toWrite := int64(buf.Len())
	for {
		n, err := io.Copy(w, buf)
		if err != nil {
			return err
		}

		toWrite -= n
		if toWrite <= 0 {
			return nil
		}
	}
}

// FileMove tries to move a file by using os.Rename,
// if that fails it tries to copy the file and remove the source.
func FileMove(oldPath string, newPath string) error {
	err := os.Rename(oldPath, newPath)
	if err == nil {
		return nil
	}

	err = FileCopy(oldPath, newPath)
	if err != nil {
		return err
	}

	os.Remove(oldPath)

	return nil
}

// FileCopy copies a file, overwriting the target if it exists.
func FileCopy(source string, dest string) error {
	s, err := os.Open(source)
	if err != nil {
		return err
	}
	defer s.Close()

	fi, err := s.Stat()
	if err != nil {
		return err
	}

	d, err := os.Create(dest)
	if err != nil {
		if os.IsExist(err) {
			d, err = os.OpenFile(dest, os.O_WRONLY, fi.Mode())
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	defer d.Close()

	_, err = io.Copy(d, s)
	if err != nil {
		return err
	}

	/* chown not supported on windows */
	if runtime.GOOS != "windows" {
		_, uid, gid := GetOwnerMode(fi)
		return d.Chown(uid, gid)
	}

	return nil
}

type BytesReadCloser struct {
	Buf *bytes.Buffer
}

func (r BytesReadCloser) Read(b []byte) (n int, err error) {
	return r.Buf.Read(b)
}

func (r BytesReadCloser) Close() error {
	/* no-op since we're in memory */
	return nil
}

func IsSnapshot(name string) bool {
	return strings.Contains(name, SnapshotDelimiter)
}

func ExtractSnapshotName(name string) string {
	return strings.SplitN(name, SnapshotDelimiter, 2)[1]
}

func MkdirAllOwner(path string, perm os.FileMode, uid int, gid int) error {
	// This function is a slightly modified version of MkdirAll from the Go standard library.
	// https://golang.org/src/os/path.go?s=488:535#L9

	// Fast path: if we can tell whether path is a directory or file, stop with success or error.
	dir, err := os.Stat(path)
	if err == nil {
		if dir.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but isn't a directory")
	}

	// Slow path: make sure parent exists and then call Mkdir for path.
	i := len(path)
	for i > 0 && os.IsPathSeparator(path[i-1]) { // Skip trailing path separator.
		i--
	}

	j := i
	for j > 0 && !os.IsPathSeparator(path[j-1]) { // Scan backward over element.
		j--
	}

	if j > 1 {
		// Create parent
		err = MkdirAllOwner(path[0:j-1], perm, uid, gid)
		if err != nil {
			return err
		}
	}

	// Parent now exists; invoke Mkdir and use its result.
	err = os.Mkdir(path, perm)

	err_chown := os.Chown(path, uid, gid)
	if err_chown != nil {
		return err_chown
	}

	if err != nil {
		// Handle arguments like "foo/." by
		// double-checking that directory doesn't exist.
		dir, err1 := os.Lstat(path)
		if err1 == nil && dir.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func StringInSlice(key string, list []string) bool {
	for _, entry := range list {
		if entry == key {
			return true
		}
	}
	return false
}

func IntInSlice(key int, list []int) bool {
	for _, entry := range list {
		if entry == key {
			return true
		}
	}
	return false
}

func Int64InSlice(key int64, list []int64) bool {
	for _, entry := range list {
		if entry == key {
			return true
		}
	}
	return false
}

func IsTrue(value string) bool {
	if StringInSlice(strings.ToLower(value), []string{"true", "1", "yes", "on"}) {
		return true
	}

	return false
}

func IsBlockdev(fm os.FileMode) bool {
	return ((fm&os.ModeDevice != 0) && (fm&os.ModeCharDevice == 0))
}

func IsBlockdevPath(pathName string) bool {
	sb, err := os.Stat(pathName)
	if err != nil {
		return false
	}

	fm := sb.Mode()
	return ((fm&os.ModeDevice != 0) && (fm&os.ModeCharDevice == 0))
}

func BlockFsDetect(dev string) (string, error) {
	out, err := RunCommand("blkid", "-s", "TYPE", "-o", "value", dev)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

// DeepCopy copies src to dest by using encoding/gob so its not that fast.
func DeepCopy(src, dest interface{}) error {
	buff := new(bytes.Buffer)
	enc := gob.NewEncoder(buff)
	dec := gob.NewDecoder(buff)
	if err := enc.Encode(src); err != nil {
		return err
	}

	if err := dec.Decode(dest); err != nil {
		return err
	}

	return nil
}

func RunningInUserNS() bool {
	file, err := os.Open("/proc/self/uid_map")
	if err != nil {
		return false
	}
	defer file.Close()

	buf := bufio.NewReader(file)
	l, _, err := buf.ReadLine()
	if err != nil {
		return false
	}

	line := string(l)
	var a, b, c int64
	fmt.Sscanf(line, "%d %d %d", &a, &b, &c)
	if a == 0 && b == 0 && c == 4294967295 {
		return false
	}
	return true
}

func ValidHostname(name string) bool {
	// Validate length
	if len(name) < 1 || len(name) > 63 {
		return false
	}

	// Validate first character
	if strings.HasPrefix(name, "-") {
		return false
	}

	if _, err := strconv.Atoi(string(name[0])); err == nil {
		return false
	}

	// Validate last character
	if strings.HasSuffix(name, "-") {
		return false
	}

	// Validate the character set
	match, _ := regexp.MatchString("^[-a-zA-Z0-9]*$", name)
	if !match {
		return false
	}

	return true
}

// Spawn the editor with a temporary YAML file for editing configs
func TextEditor(inPath string, inContent []byte) ([]byte, error) {
	var f *os.File
	var err error
	var path string

	// Detect the text editor to use
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
		if editor == "" {
			for _, p := range []string{"editor", "vi", "emacs", "nano"} {
				_, err := exec.LookPath(p)
				if err == nil {
					editor = p
					break
				}
			}
			if editor == "" {
				return []byte{}, fmt.Errorf("No text editor found, please set the EDITOR environment variable.")
			}
		}
	}

	if inPath == "" {
		// If provided input, create a new file
		f, err = ioutil.TempFile("", "lxd_editor_")
		if err != nil {
			return []byte{}, err
		}

		err = os.Chmod(f.Name(), 0600)
		if err != nil {
			f.Close()
			os.Remove(f.Name())
			return []byte{}, err
		}

		f.Write(inContent)
		f.Close()

		path = fmt.Sprintf("%s.yaml", f.Name())
		os.Rename(f.Name(), path)
		defer os.Remove(path)
	} else {
		path = inPath
	}

	cmdParts := strings.Fields(editor)
	cmd := exec.Command(cmdParts[0], append(cmdParts[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return []byte{}, err
	}

	content, err := ioutil.ReadFile(path)
	if err != nil {
		return []byte{}, err
	}

	return content, nil
}

func ParseMetadata(metadata interface{}) (map[string]interface{}, error) {
	newMetadata := make(map[string]interface{})
	s := reflect.ValueOf(metadata)
	if !s.IsValid() {
		return nil, nil
	}

	if s.Kind() == reflect.Map {
		for _, k := range s.MapKeys() {
			if k.Kind() != reflect.String {
				return nil, fmt.Errorf("Invalid metadata provided (key isn't a string).")
			}
			newMetadata[k.String()] = s.MapIndex(k).Interface()
		}
	} else if s.Kind() == reflect.Ptr && !s.Elem().IsValid() {
		return nil, nil
	} else {
		return nil, fmt.Errorf("Invalid metadata provided (type isn't a map).")
	}

	return newMetadata, nil
}

// Parse a size string in bytes (e.g. 200kB or 5GB) into the number of bytes it
// represents. Supports suffixes up to EB. "" == 0.
func ParseByteSizeString(input string) (int64, error) {
	suffixLen := 2

	if input == "" {
		return 0, nil
	}

	if unicode.IsNumber(rune(input[len(input)-1])) {
		// COMMENT(brauner): No suffix --> bytes.
		suffixLen = 0
	} else if (len(input) >= 2) && (input[len(input)-1] == 'B') && unicode.IsNumber(rune(input[len(input)-2])) {
		// COMMENT(brauner): "B" suffix --> bytes.
		suffixLen = 1
	} else if strings.HasSuffix(input, " bytes") {
		// COMMENT(brauner): Backward compatible behaviour in case we
		// talk to a LXD that still uses GetByteSizeString() that
		// returns "n bytes".
		suffixLen = 6
	} else if (len(input) < 3) && (suffixLen == 2) {
		return -1, fmt.Errorf("Invalid value: %s", input)
	}

	// Extract the suffix
	suffix := input[len(input)-suffixLen:]

	// Extract the value
	value := input[0 : len(input)-suffixLen]
	valueInt, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("Invalid integer: %s", input)
	}

	if valueInt < 0 {
		return -1, fmt.Errorf("Invalid value: %d", valueInt)
	}

	// COMMENT(brauner): The value is already in bytes.
	if suffixLen != 2 {
		return valueInt, nil
	}

	// Figure out the multiplicator
	multiplicator := int64(0)
	switch suffix {
	case "kB":
		multiplicator = 1024
	case "MB":
		multiplicator = 1024 * 1024
	case "GB":
		multiplicator = 1024 * 1024 * 1024
	case "TB":
		multiplicator = 1024 * 1024 * 1024 * 1024
	case "PB":
		multiplicator = 1024 * 1024 * 1024 * 1024 * 1024
	case "EB":
		multiplicator = 1024 * 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return -1, fmt.Errorf("Unsupported suffix: %s", suffix)
	}

	return valueInt * multiplicator, nil
}

// Parse a size string in bits (e.g. 200kbit or 5Gbit) into the number of bits
// it represents. Supports suffixes up to Ebit. "" == 0.
func ParseBitSizeString(input string) (int64, error) {
	if input == "" {
		return 0, nil
	}

	if len(input) < 5 {
		return -1, fmt.Errorf("Invalid value: %s", input)
	}

	// Extract the suffix
	suffix := input[len(input)-4:]

	// Extract the value
	value := input[0 : len(input)-4]
	valueInt, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("Invalid integer: %s", input)
	}

	if valueInt < 0 {
		return -1, fmt.Errorf("Invalid value: %d", valueInt)
	}

	// Figure out the multiplicator
	multiplicator := int64(0)
	switch suffix {
	case "kbit":
		multiplicator = 1000
	case "Mbit":
		multiplicator = 1000 * 1000
	case "Gbit":
		multiplicator = 1000 * 1000 * 1000
	case "Tbit":
		multiplicator = 1000 * 1000 * 1000 * 1000
	case "Pbit":
		multiplicator = 1000 * 1000 * 1000 * 1000 * 1000
	case "Ebit":
		multiplicator = 1000 * 1000 * 1000 * 1000 * 1000 * 1000
	default:
		return -1, fmt.Errorf("Unsupported suffix: %s", suffix)
	}

	return valueInt * multiplicator, nil
}

func GetByteSizeString(input int64, precision uint) string {
	if input < 1024 {
		return fmt.Sprintf("%dB", input)
	}

	value := float64(input)

	for _, unit := range []string{"kB", "MB", "GB", "TB", "PB", "EB"} {
		value = value / 1024
		if value < 1024 {
			return fmt.Sprintf("%.*f%s", precision, value, unit)
		}
	}

	return fmt.Sprintf("%.*fEB", precision, value)
}

type RunError struct {
	msg string
	Err error
}

func (e RunError) Error() string {
	return e.msg
}

func RunCommand(name string, arg ...string) (string, error) {
	output, err := exec.Command(name, arg...).CombinedOutput()
	if err != nil {
		err := RunError{
			msg: fmt.Sprintf("Failed to run: %s %s: %s", name, strings.Join(arg, " "), strings.TrimSpace(string(output))),
			Err: err,
		}
		return string(output), err
	}

	return string(output), nil
}

func TryRunCommand(name string, arg ...string) (string, error) {
	var err error
	var output string

	for i := 0; i < 20; i++ {
		output, err = RunCommand(name, arg...)
		if err == nil {
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	return output, err
}

func TimeIsSet(ts time.Time) bool {
	if ts.Unix() <= 0 {
		return false
	}

	if ts.UTC().Unix() <= 0 {
		return false
	}

	return true
}

// WriteTempFile creates a temp file with the specified content
func WriteTempFile(dir string, prefix string, content string) (string, error) {
	f, err := ioutil.TempFile(dir, prefix)
	if err != nil {
		return "", err
	}
	defer f.Close()

	_, err = f.WriteString(content)
	return f.Name(), err
}

// EscapePathFstab escapes a path fstab-style.
// This ensures that getmntent_r() and friends can correctly parse stuff like
// /some/wacky path with spaces /some/wacky target with spaces
func EscapePathFstab(path string) string {
	r := strings.NewReplacer(
		" ", "\\040",
		"\t", "\\011",
		"\n", "\\012",
		"\\", "\\\\")
	return r.Replace(path)
}

func DownloadFileSha256(httpClient *http.Client, useragent string, progress func(progress ioprogress.ProgressData), canceler *cancel.Canceler, filename string, url string, hash string, target io.WriteSeeker) (int64, error) {
	// Always seek to the beginning
	target.Seek(0, 0)

	// Prepare the download request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return -1, err
	}

	if useragent != "" {
		req.Header.Set("User-Agent", useragent)
	}

	// Perform the request
	r, doneCh, err := cancel.CancelableDownload(canceler, httpClient, req)
	if err != nil {
		return -1, err
	}
	defer r.Body.Close()
	defer close(doneCh)

	if r.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("Unable to fetch %s: %s", url, r.Status)
	}

	// Handle the data
	body := r.Body
	if progress != nil {
		body = &ioprogress.ProgressReader{
			ReadCloser: r.Body,
			Tracker: &ioprogress.ProgressTracker{
				Length: r.ContentLength,
				Handler: func(percent int64, speed int64) {
					if filename != "" {
						progress(ioprogress.ProgressData{Text: fmt.Sprintf("%s: %d%% (%s/s)", filename, percent, GetByteSizeString(speed, 2))})
					} else {
						progress(ioprogress.ProgressData{Text: fmt.Sprintf("%d%% (%s/s)", percent, GetByteSizeString(speed, 2))})
					}
				},
			},
		}
	}

	sha256 := sha256.New()
	size, err := io.Copy(io.MultiWriter(target, sha256), body)
	if err != nil {
		return -1, err
	}

	result := fmt.Sprintf("%x", sha256.Sum(nil))
	if result != hash {
		return -1, fmt.Errorf("Hash mismatch for %s: %s != %s", url, result, hash)
	}

	return size, nil
}
