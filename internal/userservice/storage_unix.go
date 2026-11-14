//go:build darwin || linux

package userservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	ReceiptSchema        = "https://schemas.3leaps.dev/spanwit/user-service-receipt/v0.json"
	serviceControlLock   = "service-control.lock"
	serviceReceiptName   = "service-receipt.json"
	maxReceiptBytes      = 64 << 10
	maxDefinitionReadCap = 128 << 10
)

type serviceReceipt struct {
	Schema                   string   `json:"$schema"`
	Version                  int      `json:"version"`
	State                    string   `json:"state"`
	Platform                 string   `json:"platform"`
	Manager                  string   `json:"manager"`
	Label                    string   `json:"label"`
	UID                      int      `json:"uid"`
	Home                     string   `json:"home"`
	ConfigHome               string   `json:"config_home,omitempty"`
	StateDir                 string   `json:"state_dir"`
	SamplePath               string   `json:"sample_path"`
	Interval                 string   `json:"interval"`
	Roots                    []string `json:"roots"`
	BinaryName               string   `json:"binary_name"`
	Vendor                   string   `json:"vendor"`
	DefinitionPath           string   `json:"definition_path"`
	DefinitionSHA256         string   `json:"definition_sha256,omitempty"`
	DefinitionDev            string   `json:"definition_dev,omitempty"`
	DefinitionIno            string   `json:"definition_ino,omitempty"`
	PendingDefinitionSHA256  string   `json:"pending_definition_sha256,omitempty"`
	PreviousDefinitionSHA256 string   `json:"previous_definition_sha256,omitempty"`
	ExecutablePath           string   `json:"executable_path"`
	ExecutableSHA256         string   `json:"executable_sha256"`
	ExecutableSize           int64    `json:"executable_size"`
	ExecutableDev            string   `json:"executable_dev"`
	ExecutableIno            string   `json:"executable_ino"`
	ConfigSHA256             string   `json:"config_sha256"`
	UpdatedAt                string   `json:"updated_at"`
}

type fileIdentity struct {
	SHA256 string
	Dev    string
	Ino    string
}

func acquireServiceControlLock(stateDir string) (*daemon.Ownership, error) {
	return daemon.AcquireNamedLock(stateDir, serviceControlLock)
}

func readReceiptReadOnly(stateDir string) (*serviceReceipt, bool, error) {
	dirFD, canonical, err := daemon.OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	raw, exists, err := readVerifiedRegularFile(dirFD, serviceReceiptName, maxReceiptBytes)
	if err != nil || !exists {
		return nil, exists, err
	}
	var r serviceReceipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, true, fmt.Errorf("decode service receipt: %w", err)
	}
	if err := validateReceipt(r, canonical); err != nil {
		return nil, true, err
	}
	return &r, true, nil
}

func validateReceipt(r serviceReceipt, canonicalStateDir string) error {
	if r.Schema != ReceiptSchema || r.Version != 0 {
		return fmt.Errorf("unsupported service receipt schema")
	}
	if r.State != "installing" && r.State != "installed" && r.State != "uninstalling" {
		return fmt.Errorf("invalid service receipt state %q", r.State)
	}
	if r.UID != os.Getuid() {
		return fmt.Errorf("service receipt UID mismatch")
	}
	if r.StateDir != canonicalStateDir {
		return fmt.Errorf("service receipt state directory mismatch")
	}
	if err := validatePath("receipt home", r.Home, maxPathBytes); err != nil {
		return err
	}
	if r.ConfigHome != "" {
		if err := validatePath("receipt config home", r.ConfigHome, maxPathBytes); err != nil {
			return err
		}
	}
	if !validLabelPart(r.Vendor) || !validLabelPart(r.BinaryName) {
		return fmt.Errorf("invalid service receipt vendor or binary name")
	}
	expectedLabel := r.Vendor + "." + r.BinaryName + ".daemon"
	if r.Label != expectedLabel {
		return fmt.Errorf("service receipt label mismatch")
	}
	expectedMgr, err := managerNameForPlatform(r.Platform)
	if err != nil || r.Manager != expectedMgr {
		return fmt.Errorf("invalid service receipt platform or manager")
	}
	expectedDefPath, _, _, ok := defaultDefinitionTarget(Options{
		Platform:   r.Platform,
		Home:       r.Home,
		ConfigHome: r.ConfigHome,
		Vendor:     r.Vendor,
		BinaryName: r.BinaryName,
	})
	if !ok || r.DefinitionPath != expectedDefPath {
		return fmt.Errorf("service receipt definition path mismatch")
	}
	if err := validatePath("receipt definition path", r.DefinitionPath, maxPathBytes); err != nil {
		return err
	}
	if err := validatePath("receipt executable path", r.ExecutablePath, maxPathBytes); err != nil {
		return err
	}
	if err := validatePath("receipt sample path", r.SamplePath, maxPathBytes); err != nil {
		return err
	}
	dur, err := time.ParseDuration(r.Interval)
	if err != nil || dur < time.Minute {
		return fmt.Errorf("invalid service receipt interval")
	}
	if len(r.Roots) > 16 {
		return fmt.Errorf("too many service receipt roots")
	}
	for _, root := range r.Roots {
		if err := validatePath("receipt root", root, maxPathBytes); err != nil {
			return err
		}
	}
	if !isLowerHex64(r.ConfigSHA256) || !isLowerHex64(r.ExecutableSHA256) {
		return fmt.Errorf("invalid service receipt digest")
	}
	if r.DefinitionSHA256 != "" && !isLowerHex64(r.DefinitionSHA256) {
		return fmt.Errorf("invalid service receipt definition digest")
	}
	if r.PendingDefinitionSHA256 != "" && !isLowerHex64(r.PendingDefinitionSHA256) {
		return fmt.Errorf("invalid service receipt pending definition digest")
	}
	if r.PreviousDefinitionSHA256 != "" && !isLowerHex64(r.PreviousDefinitionSHA256) {
		return fmt.Errorf("invalid service receipt previous definition digest")
	}
	if r.State == "installed" && !isLowerHex64(r.DefinitionSHA256) {
		return fmt.Errorf("installed service receipt missing definition digest")
	}
	if r.State == "installing" && !isLowerHex64(r.PendingDefinitionSHA256) {
		return fmt.Errorf("installing service receipt missing pending definition digest")
	}
	if r.State == "uninstalling" && r.DefinitionSHA256 == "" && r.PendingDefinitionSHA256 == "" && r.PreviousDefinitionSHA256 == "" {
		return fmt.Errorf("uninstalling service receipt missing definition digest")
	}
	return nil
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func writeReceiptAtomic(stateDir string, r serviceReceipt) error {
	dirFD, canonical, err := daemon.OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	r.Schema = ReceiptSchema
	r.Version = 0
	r.StateDir = canonical
	r.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxReceiptBytes {
		return fmt.Errorf("service receipt exceeds %d bytes", maxReceiptBytes)
	}
	_, err = writeFileAtomicInDir(dirFD, serviceReceiptName, raw)
	return err
}

func removeReceiptInDir(stateDir string) error {
	dirFD, _, err := daemon.OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()
	if err := unix.Unlinkat(dirFD, serviceReceiptName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return unix.Fsync(dirFD)
}

func inspectDefinitionFile(defPath string) (fileIdentity, bool, error) {
	if err := validatePath("definition path", defPath, maxPathBytes); err != nil {
		return fileIdentity{}, false, err
	}
	dir := filepath.Dir(defPath)
	base := filepath.Base(defPath)
	dirFD, _, err := daemon.OpenVerifiedDir(dir, false, 0755)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
			return fileIdentity{}, false, nil
		}
		return fileIdentity{}, false, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	return inspectRegularFileInDir(dirFD, base, maxDefinitionReadCap)
}

func writeDefinitionAtomic(defPath string, content []byte) (fileIdentity, error) {
	if err := validatePath("definition path", defPath, maxPathBytes); err != nil {
		return fileIdentity{}, err
	}
	dir := filepath.Dir(defPath)
	base := filepath.Base(defPath)
	dirFD, _, err := daemon.OpenVerifiedDir(dir, true, 0755)
	if err != nil {
		return fileIdentity{}, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	return writeFileAtomicInDir(dirFD, base, content)
}

func removeDefinitionFile(defPath, expectedSHA256 string, allowedAlternateSHA256 ...string) (bool, error) {
	if err := validatePath("definition path", defPath, maxPathBytes); err != nil {
		return false, err
	}
	dir := filepath.Dir(defPath)
	base := filepath.Base(defPath)
	dirFD, _, err := daemon.OpenVerifiedDir(dir, false, 0755)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	id, exists, err := inspectRegularFileInDir(dirFD, base, maxDefinitionReadCap)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	matches := expectedSHA256 != "" && id.SHA256 == expectedSHA256
	for _, alt := range allowedAlternateSHA256 {
		if alt != "" && id.SHA256 == alt {
			matches = true
		}
	}
	if !matches {
		return false, fmt.Errorf("refusing to remove modified or foreign definition file")
	}
	if err := unix.Unlinkat(dirFD, base, 0); err != nil {
		return false, err
	}
	if err := unix.Fsync(dirFD); err != nil {
		return true, err
	}
	return true, nil
}

func readVerifiedRegularFile(dirFD int, name string, maxBytes int64) ([]byte, bool, error) {
	var lst unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &lst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if lst.Mode&unix.S_IFMT != unix.S_IFREG || lst.Nlink != 1 || int64(lst.Uid) != int64(os.Getuid()) || lst.Mode&0022 != 0 {
		return nil, true, fmt.Errorf("unsafe file %s", name)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, true, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, true, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || int64(st.Uid) != int64(os.Getuid()) || st.Mode&0022 != 0 {
		return nil, true, fmt.Errorf("unsafe file %s", name)
	}
	if st.Size < 0 || st.Size > maxBytes {
		return nil, true, fmt.Errorf("file %s size out of bounds", name)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, true, fmt.Errorf("file %s exceeds limit", name)
	}
	return raw, true, nil
}

func inspectRegularFileInDir(dirFD int, name string, maxBytes int64) (fileIdentity, bool, error) {
	var lst unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &lst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return fileIdentity{}, false, nil
		}
		return fileIdentity{}, false, err
	}
	if lst.Mode&unix.S_IFMT != unix.S_IFREG || lst.Nlink != 1 || int64(lst.Uid) != int64(os.Getuid()) || lst.Mode&0022 != 0 {
		return fileIdentity{}, true, fmt.Errorf("unsafe definition file %s", name)
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return fileIdentity{}, true, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fileIdentity{}, true, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || int64(st.Uid) != int64(os.Getuid()) || st.Mode&0022 != 0 {
		return fileIdentity{}, true, fmt.Errorf("unsafe definition file %s", name)
	}
	if st.Size <= 0 || st.Size > maxBytes {
		return fileIdentity{}, true, fmt.Errorf("definition file %s size out of bounds", name)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return fileIdentity{}, true, err
	}
	if int64(len(raw)) != st.Size {
		return fileIdentity{}, true, fmt.Errorf("definition file %s changed during read", name)
	}
	sum := sha256.Sum256(raw)
	return fileIdentity{
		SHA256: hex.EncodeToString(sum[:]),
		Dev:    fmt.Sprintf("%d", st.Dev),
		Ino:    fmt.Sprintf("%d", st.Ino),
	}, true, nil
}

func writeFileAtomicInDir(dirFD int, name string, content []byte) (fileIdentity, error) {
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return fileIdentity{}, fmt.Errorf("invalid target filename")
	}
	tmpName := fmt.Sprintf(".%s.tmp-%s", name, uuid.NewString())
	tmpFD, err := unix.Openat(dirFD, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fileIdentity{}, err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = unix.Unlinkat(dirFD, tmpName, 0)
		}
	}()
	tmpFile := os.NewFile(uintptr(tmpFD), tmpName)
	n, writeErr := tmpFile.Write(content)
	if writeErr == nil && n != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = unix.Fsync(tmpFD)
	}
	closeErr := tmpFile.Close()
	if writeErr != nil {
		return fileIdentity{}, writeErr
	}
	if closeErr != nil {
		return fileIdentity{}, closeErr
	}
	if err := unix.Renameat(dirFD, tmpName, dirFD, name); err != nil {
		return fileIdentity{}, err
	}
	cleanupTmp = false
	if err := unix.Fsync(dirFD); err != nil {
		return fileIdentity{}, err
	}
	id, exists, err := inspectRegularFileInDir(dirFD, name, int64(len(content))+1024)
	if err != nil {
		return fileIdentity{}, err
	}
	if !exists {
		return fileIdentity{}, fmt.Errorf("staged file %s missing after rename", name)
	}
	sum := sha256.Sum256(content)
	expected := hex.EncodeToString(sum[:])
	if id.SHA256 != expected {
		return fileIdentity{}, fmt.Errorf("staged file %s digest mismatch after rename", name)
	}
	return id, nil
}

func socketExistsReadOnly(stateDir string) (bool, error) {
	dirFD, _, err := daemon.OpenVerifiedDir(stateDir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	var st unix.Stat_t
	if err := unix.Fstatat(dirFD, daemon.SocketName, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
