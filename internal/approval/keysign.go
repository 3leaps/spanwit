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
	"strings"
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
// other path must be absolute, resolve to a regular executable file that
// neither group nor others can write. Signing never searches PATH; the
// result only affects how the signature is produced, never how it is
// verified.
func SignerPath(path string) (string, error) {
	if path == "" {
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
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: --ssh-keygen %s is not an executable file", ErrSignerUnsupported, path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%w: --ssh-keygen %s is group or other writable", ErrSignerUnsupported, path)
	}
	return resolved, nil
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
		return fmt.Errorf("%w: %s cannot use security keys. Use an OpenSSH build with security-key support "+
			"and pass it with --ssh-keygen (on macOS: brew install openssh, then --ssh-keygen /opt/homebrew/bin/ssh-keygen)",
			ErrSignerUnsupported, program)
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
