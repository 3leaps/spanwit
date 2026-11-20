//go:build darwin || linux

package approval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// signTimeout bounds one signing attempt: room to enter a PIN or key-file
// passphrase and touch the key, but not an unattended wait.
var signTimeout = 2 * time.Minute

const maxSignOutput = 64 << 10

// ErrSignerUnsupported reports that the ssh-keygen used for signing cannot
// talk to a security key.
var ErrSignerUnsupported = errors.New("signer_unsupported")

// SignerPath resolves the ssh-keygen used to sign. Empty means the system
// ssh-keygen, which must pass the same trust check as verification. Any
// other path must be absolute and clean; it is resolved, and the resolved
// path (the one executed) must be one only this user or an administrator
// can change: every component owned by this user or root, none writable by
// others, group write only for an administrative group, and the file a
// regular executable without set-id bits. The signer reads the key's PIN,
// so a binary another user could replace is refused even though the
// signature it produces is verified afterwards. Signing never searches
// PATH; verification always uses the system ssh-keygen.
func SignerPath(path string) (string, error) {
	if path == "" {
		if systemSignerLacksSecurityKeys {
			return "", unsupportedSigner(sshKeygenPath)
		}
		if _, err := TrustedFile(sshKeygenPath, 64<<20); err != nil {
			return "", fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
		}
		return sshKeygenPath, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: --ssh-keygen %s is not an absolute clean path", ErrSignerUnsupported, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: --ssh-keygen %s: %v", ErrSignerUnsupported, path, err)
	}
	for cur := resolved; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("%w: --ssh-keygen %s: %v", ErrSignerUnsupported, path, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", fmt.Errorf("%w: --ssh-keygen %s: no ownership for %s", ErrSignerUnsupported, path, cur)
		}
		if reason := signerEntryProblem(info.Mode(), st.Uid, st.Gid, cur == resolved); reason != "" {
			return "", fmt.Errorf("%w: --ssh-keygen %s: %s %s", ErrSignerUnsupported, path, cur, reason)
		}
		if cur == filepath.Dir(cur) {
			break
		}
	}
	return resolved, nil
}

// systemSignerLacksSecurityKeys is true where the system ssh-keygen is built
// without security-key support. On macOS it is, and Sign runs it with an empty
// environment, so no provider can be supplied either. Refusing here happens
// before the confirmation prompt and before ssh-keygen asks for a PIN it
// cannot use. Tests replace it.
var systemSignerLacksSecurityKeys = runtime.GOOS == "darwin"

// unsupportedSigner names program and how to choose one that works.
func unsupportedSigner(program string) error {
	return fmt.Errorf("%w: %s cannot use security keys. Use an OpenSSH build with security-key support "+
		"and pass it with --ssh-keygen (on macOS: brew install openssh, then --ssh-keygen /opt/homebrew/bin/ssh-keygen)",
		ErrSignerUnsupported, program)
}

// signerOwnerOK accepts this user and root. Tests replace it.
var signerOwnerOK = func(uid uint32) bool { return uid == 0 || int(uid) == os.Geteuid() }

// adminGroup reports whether gid is a group whose members administer the
// machine anyway: wheel (0) everywhere, and admin (80) on macOS, which
// Homebrew gives group write on its prefix.
func adminGroup(gid uint32) bool {
	return gid == 0 || (runtime.GOOS == "darwin" && gid == 80)
}

// signerEntryProblem returns why one resolved path component is not
// acceptable for the signing binary, or "" when it is.
func signerEntryProblem(mode os.FileMode, uid, gid uint32, final bool) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "changed while resolving"
	case !signerOwnerOK(uid):
		return "is owned by another user"
	case mode.Perm()&0o002 != 0:
		return "is writable by others"
	case mode.Perm()&0o020 != 0 && !adminGroup(gid):
		return "is group writable"
	}
	if final {
		switch {
		case !mode.IsRegular() || mode.Perm()&0o111 == 0:
			return "is not an executable file"
		case mode&(os.ModeSetuid|os.ModeSetgid) != 0:
			return "is set-id"
		}
	}
	return ""
}

// signWithKeygen runs `ssh-keygen -Y sign` with the payload on standard
// input. The child stays in the caller's process group, so when spanwit runs
// in the foreground of a terminal ssh-keygen can prompt there for a security
// key PIN or a key-file passphrase. The environment is empty: no agent and
// no provider override, so the key is used through the binary's own
// security-key support.
var signWithKeygen = func(ctx context.Context, program, keyPath string, payload []byte) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, signTimeout)
	defer cancel()
	// #nosec G204 -- program is SignerPath-checked; argv is fixed apart from the key path, no shell.
	cmd := exec.CommandContext(ctx, program, "-Y", "sign", "-n", SignatureNamespace, "-f", keyPath)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = maxSignOutput, maxSignOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", signTimeout)
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// signFailure turns ssh-keygen's stderr into a refusal that says what to
// do. Messages are OpenSSH's own wording.
func signFailure(program string, stderr []byte, err error) error {
	text := string(stderr)
	switch {
	case strings.Contains(text, "internal security key support not enabled"),
		strings.Contains(text, "No FIDO SecurityKeyProvider specified"):
		return unsupportedSigner(program)
	case strings.Contains(text, "device not found"):
		return fmt.Errorf("ssh-keygen sign: no security key found; insert the key and try again")
	}
	if line := lastLine(stderr); line != "" {
		return fmt.Errorf("ssh-keygen sign: %s", line)
	}
	return fmt.Errorf("ssh-keygen sign: %v", err)
}

// lastLine is the last non-empty line: ssh-keygen prints progress such as
// "Confirm user presence" before the error that matters.
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
