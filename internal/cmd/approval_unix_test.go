//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"

	"github.com/3leaps/spanwit/internal/approval"
)

func runApprovalCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newApprovalCmd(&appidentity.Identity{BinaryName: "spanwit", EnvPrefix: "SPANWIT_"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func approvalCLIRig(t *testing.T) (stateDir, plan, dir string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(base, 0o700)
	stateDir = filepath.Join(base, "state")
	plan = filepath.Join(base, "plan.json")
	doc := `{"$schema":"x","version":1,"analysis_root":"` + base + `","min_size":"0","sort_mode":"size","reclaim_scope":"whole","candidates":[{"path":"` + filepath.Join(base, "victim") + `","signature":"development.rust.cargo-target","size_bytes":4096}]}`
	if err := os.WriteFile(plan, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "victim"), 0o700); err != nil {
		t.Fatal(err)
	}
	return stateDir, plan, base
}

func TestApprovalCLIRequestShowIssueRevoke(t *testing.T) {
	stateDir, plan, dir := approvalCLIRig(t)
	payload := filepath.Join(dir, "payload.json")
	out, err := runApprovalCmd(t, "request", "--state-dir", stateDir, "--plan", plan, "--out", payload, "--expires", "30m")
	if err != nil {
		t.Fatalf("request: %v\n%s", err, out)
	}
	if !strings.Contains(out, "approval sign --plan") || !strings.Contains(out, "no authority until issued") {
		t.Fatalf("request output does not name the required step:\n%s", out)
	}
	raw, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	p, err := approval.ParsePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	// request refuses to overwrite an existing payload file
	if _, err := runApprovalCmd(t, "request", "--state-dir", stateDir, "--plan", plan, "--out", payload); err == nil {
		t.Fatal("request overwrote an existing payload")
	}
	out, err = runApprovalCmd(t, "show", "--plan", plan, "--payload", payload)
	if err != nil || !strings.Contains(out, p.ApprovalID) || !strings.Contains(out, "victim") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	// issue against an allowed-signers file this user owns: untrusted
	signers := filepath.Join(dir, "allowed_signers")
	_ = os.WriteFile(signers, []byte("m sk-ssh-ed25519@openssh.com AAAA\n"), 0o644)
	sig := filepath.Join(dir, "payload.sig")
	_ = os.WriteFile(sig, []byte("-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----\n"), 0o600)
	_, err = runApprovalCmd(t, "issue", "--state-dir", stateDir, "--payload", payload, "--sig", sig, "--allowed-signers", signers)
	if approval.RefusalCode(err) != approval.CodeAllowedSignersUntrusted {
		t.Fatalf("issue with an untrusted signers file: %v", err)
	}
	if _, err := runApprovalCmd(t, "revoke", "--state-dir", stateDir, p.ApprovalID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	out, err = runApprovalCmd(t, "status", "--state-dir", stateDir, p.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil || st["state"] != "revoked" {
		t.Fatalf("status %v %s", err, out)
	}
	// prune under the revoked approval: refused before any effect, nothing deleted
	identity, _, _, _ := journalRig(t)
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-exact-plan", plan, "--execute", "--approval", p.ApprovalID, "--state-dir", stateDir, "--allowed-signers", signers})
	var runErr error
	stdout := captureStdout(t, func() { runErr = cmd.Execute() })
	if approval.RefusalCode(runErr) != approval.CodeGrantExpiredOrRevoked || strings.TrimSpace(stdout) != "" {
		t.Fatalf("prune under revoked approval: %v stdout=%q", runErr, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "victim")); err != nil {
		t.Fatalf("victim touched: %v", err)
	}
}

func TestApprovalCLIShowRefusesMismatchedPlan(t *testing.T) {
	stateDir, plan, dir := approvalCLIRig(t)
	payload := filepath.Join(dir, "payload.json")
	if out, err := runApprovalCmd(t, "request", "--state-dir", stateDir, "--plan", plan, "--out", payload); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	raw, _ := os.ReadFile(plan)
	_ = os.WriteFile(plan, bytes.Replace(raw, []byte(`4096`), []byte(`4097`), 1), 0o600)
	if _, err := runApprovalCmd(t, "show", "--plan", plan, "--payload", payload); approval.RefusalCode(err) != approval.CodeStalePlanOrDrift {
		t.Fatalf("show accepted a changed plan: %v", err)
	}
}

func TestApprovalCLISignNeedsTerminal(t *testing.T) {
	stateDir, plan, dir := approvalCLIRig(t)
	payload := filepath.Join(dir, "payload.json")
	if _, err := runApprovalCmd(t, "request", "--state-dir", stateDir, "--plan", plan, "--out", payload); err != nil {
		t.Fatal(err)
	}
	devnull, _ := os.Open(os.DevNull)
	defer func() { _ = devnull.Close() }()
	old := os.Stdin
	os.Stdin = devnull
	defer func() { os.Stdin = old }()
	if _, err := runApprovalCmd(t, "sign", "--plan", plan, "--payload", payload, "--key", "/nonexistent"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("sign without a terminal: %v", err)
	}
	if _, err := os.Stat(payload + ".sig"); !os.IsNotExist(err) {
		t.Fatal("a signature was written")
	}
}
