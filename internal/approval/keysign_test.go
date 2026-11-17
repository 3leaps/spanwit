//go:build darwin || linux

package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	good := writeExec(t, dir, "ssh-keygen", "#!/bin/sh\n", 0o755)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(good)
	for in, want := range map[string]string{good: resolved, link: resolved} {
		got, err := SignerPath(in)
		if err != nil || got != want {
			t.Fatalf("SignerPath(%s) = %q, %v", in, got, err)
		}
	}
	refused := map[string]string{
		"relative":       "ssh-keygen",
		"unclean":        dir + "/./ssh-keygen",
		"missing":        filepath.Join(dir, "absent"),
		"not executable": writeExec(t, dir, "plain", "x", 0o644),
		"group writable": writeExec(t, dir, "gw", "#!/bin/sh\n", 0o775),
		"directory":      dir,
	}
	for name, in := range refused {
		if _, err := SignerPath(in); !errors.Is(err, ErrSignerUnsupported) {
			t.Errorf("%s: SignerPath(%s) = %v", name, in, err)
		}
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
