//go:build darwin || linux

package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeExec(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSignerPath(t *testing.T) {
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh")
	}
	if got, err := SignerPath(sh); err != nil || got != sh {
		t.Fatalf("root-owned system binary: %q, %v", got, err)
	}
	dir := t.TempDir()
	for name, in := range map[string]string{
		"relative": "ssh-keygen",
		"unclean":  dir + "/./ssh-keygen",
		"missing":  filepath.Join(dir, "absent"),
	} {
		if _, err := SignerPath(in); !errors.Is(err, ErrSignerUnsupported) {
			t.Errorf("%s: SignerPath(%s) = %v", name, in, err)
		}
	}
	// Another user's binary is refused even when nothing is group writable.
	prev := signerOwnerOK
	signerOwnerOK = func(uid uint32) bool { return uid != 0 }
	t.Cleanup(func() { signerOwnerOK = prev })
	if _, err := SignerPath(sh); !errors.Is(err, ErrSignerUnsupported) || !strings.Contains(err.Error(), "owned by another user") {
		t.Errorf("other owner accepted: %v", err)
	}
}

func TestSignerEntryProblem(t *testing.T) {
	prev := signerOwnerOK
	signerOwnerOK = func(uid uint32) bool { return uid == 501 || uid == 0 }
	t.Cleanup(func() { signerOwnerOK = prev })
	const me, other, staff = 501, 502, 20
	cases := []struct {
		name     string
		mode     os.FileMode
		uid, gid uint32
		final    bool
		want     string
	}{
		{"own dir", os.ModeDir | 0o755, me, staff, false, ""},
		{"root dir", os.ModeDir | 0o755, 0, 0, false, ""},
		{"other owner dir", os.ModeDir | 0o755, other, staff, false, "owned by another user"},
		{"other-writable dir", os.ModeDir | 0o777, me, staff, false, "writable by others"},
		{"sticky world dir", os.ModeDir | os.ModeSticky | 0o777, 0, 0, false, "writable by others"},
		{"group-writable staff dir", os.ModeDir | 0o775, me, staff, false, "group writable"},
		{"group-writable wheel dir", os.ModeDir | 0o775, 0, 0, false, ""},
		{"own executable", 0o555, me, staff, true, ""},
		{"not executable", 0o644, me, staff, true, "not an executable"},
		{"directory as final", os.ModeDir | 0o755, me, staff, true, "not an executable"},
		{"setuid", os.ModeSetuid | 0o755, me, staff, true, "set-id"},
		{"setgid", os.ModeSetgid | 0o755, me, staff, true, "set-id"},
		{"symlink", os.ModeSymlink | 0o755, me, staff, false, "changed while resolving"},
		{"other owner file", 0o755, other, staff, true, "owned by another user"},
	}
	for _, c := range cases {
		got := signerEntryProblem(c.mode, c.uid, c.gid, c.final)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Homebrew's prefix: group admin (80) with group write, on macOS only.
	got := signerEntryProblem(os.ModeDir|0o775, me, 80, false)
	if (runtime.GOOS == "darwin") != (got == "") {
		t.Errorf("admin group-writable dir on %s: %q", runtime.GOOS, got)
	}
}

func TestSignFailureMessages(t *testing.T) {
	for _, stderr := range []string{
		"Confirm user presence for key ED25519-SK SHA256:x\nNo FIDO SecurityKeyProvider specified\nCouldn't sign message: invalid format\n",
		"internal security key support not enabled\n",
	} {
		err := signFailure("/usr/bin/ssh-keygen", []byte(stderr), errors.New("exit status 255"))
		if !errors.Is(err, ErrSignerUnsupported) || !strings.Contains(err.Error(), "--ssh-keygen") {
			t.Errorf("%q -> %v", stderr, err)
		}
	}
	err := signFailure("/x", []byte("Confirm user presence for key\nCouldn't sign message: incorrect passphrase supplied to decrypt private key\n"), errors.New("exit 255"))
	if !strings.Contains(err.Error(), "incorrect passphrase") {
		t.Errorf("last line not reported: %v", err)
	}
	if err := signFailure("/x", nil, errors.New("timed out")); !strings.Contains(err.Error(), "timed out") {
		t.Errorf("no stderr: %v", err)
	}
}

// TestSignWithKeygenEnvironment runs a fake ssh-keygen that records its
// argv, environment and stdin, and returns a canned signature.
func TestSignWithKeygenEnvironment(t *testing.T) {
	dir := t.TempDir()
	k := newSoftSK(t)
	payload := []byte(`{"approval_id":"a1"}`)
	sig := filepath.Join(dir, "sig")
	if err := os.WriteFile(sig, k.sign(payload, SignatureNamespace, 0x01), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(dir, "rec")
	prog := writeExec(t, dir, "ssh-keygen", "#!/bin/sh\necho \"$@\" > "+rec+".args\nenv > "+rec+".env\ncat > "+rec+".in\ncat "+sig+"\n", 0o755)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-pass")
	t.Setenv("SSH_SK_PROVIDER", "/tmp/should-not-pass")
	out, stderr, err := signWithKeygen(context.Background(), prog, "/k/id_sk", payload)
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if _, err := ParseSignature(out); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(rec + ".args")
	if strings.TrimSpace(string(args)) != "-Y sign -n "+SignatureNamespace+" -f /k/id_sk" {
		t.Errorf("args %q", args)
	}
	env, _ := os.ReadFile(rec + ".env")
	if strings.Contains(string(env), "SSH_") {
		t.Errorf("environment leaked: %s", env)
	}
	in, _ := os.ReadFile(rec + ".in")
	if string(in) != string(payload) {
		t.Errorf("stdin %q", in)
	}
}
