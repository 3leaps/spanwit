package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// A1: a Go project root that a misconfigured GOCACHE=<project> (e.g. Makefile
// `export GOCACHE=$(CURDIR)`) has seeded with README/trim.txt/shards. Go writes
// README+shards into an EXISTING dir. Source + .git must never be prunable.
func TestRelocatedRegress_A1_GoProjectRootSeededByGOCACHE(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	proj := filepath.Join(root, "myproj")
	writeRegular(t, filepath.Join(proj, "go.mod"))
	writeRegular(t, filepath.Join(proj, "main.go"))
	writeRegular(t, filepath.Join(proj, ".git", "config"))
	relocatedGo(t, proj)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), proj)
}

// A1b: same for an ancestor of home-like data (a whole workspace dir).
func TestRelocatedRegress_A1b_WorkspaceSeededByGOCACHE(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	ws := filepath.Join(root, "dev")
	writeRegular(t, filepath.Join(ws, "repoA", "go.mod"))
	writeRegular(t, filepath.Join(ws, "repoA", ".git", "HEAD"))
	writeRegular(t, filepath.Join(ws, "notes", "important.txt"))
	relocatedGo(t, ws)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), ws)
}

// A2: a Cargo project root carrying a cargo CACHEDIR.TAG and debug/ (tag copied
// or target-dir later reused) is deleted whole with its sources.
func TestRelocatedRegress_A2_CargoProjectRootWithTag(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	proj := filepath.Join(root, "crate")
	writeRegular(t, filepath.Join(proj, "Cargo.toml"))
	writeRegular(t, filepath.Join(proj, "src", "main.rs"))
	writeRegular(t, filepath.Join(proj, ".git", "HEAD"))
	relocatedCargo(t, proj)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), proj)
}

// A3: GOCACHE names a symlink to the live cache. Lstat(link) is not SameFile
// with the directory, so the live cache is not excluded.
func TestRelocatedRegress_A3_GOCACHESymlinkLeaf(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	live := relocatedGo(t, filepath.Join(root, "scan", "fast", "gocache"))
	other := t.TempDir()
	link := filepath.Join(other, "gocache-link")
	if err := os.Symlink(live, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCACHE", link)
	assertAbsent(t, r4bJobs(t, builtInConfig(filepath.Join(root, "scan"))), live)
}

// A4: the default home cache is a symlink to another volume (common for big
// caches); scanning that volume finds the live default cache un-excluded.
func TestRelocatedRegress_A4_HomeCacheSymlinkedElsewhere(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	notSharedTemp(t)
	setHome(t, home)
	t.Setenv("GOCACHE", "")
	live := relocatedGo(t, filepath.Join(root, "vol", "go-build"))
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(live, filepath.Join(home, "Library", "Caches", "go-build"))
	_ = os.Symlink(live, filepath.Join(home, ".cache", "go-build"))
	assertAbsent(t, r4bJobs(t, builtInConfig(filepath.Join(root, "vol"))), live)
}

// A5: GOCACHE set with `go env -w` lives in the GOENV file, not the process
// environment; the live cache is not excluded.
func TestRelocatedRegress_A5_GOCACHEFromGoEnvFile(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	live := relocatedGo(t, filepath.Join(root, "scan", "gocache"))
	envFile := filepath.Join(t.TempDir(), "env")
	writeContent(t, envFile, "GOCACHE="+live+"\n")
	t.Setenv("GOENV", envFile)
	assertAbsent(t, r4bJobs(t, builtInConfig(filepath.Join(root, "scan"))), live)
}

// A6: debug as a regular FILE satisfies the cargo child evidence.
func TestRelocatedRegress_A6_DebugAsFile(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), cargoTagContent)
	writeRegular(t, filepath.Join(dir, "debug"))
	writeRegular(t, filepath.Join(dir, "precious.txt"))
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// A7: shard 00 as a regular FILE satisfies the Go shard evidence.
func TestRelocatedRegress_A7_ShardAsFile(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
	writeRegular(t, filepath.Join(dir, "trim.txt"))
	writeRegular(t, filepath.Join(dir, "00"))
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// Marker edge cases that must be refused (expected to pass = no bypass).
func TestRelocatedRegress_MarkerEdgesRefused(t *testing.T) {
	isolateLiveCaches(t)
	sig := "Signature: 8a477f597d28d172789f06886806bc55"
	cmt := "# This file is a cache directory tag created by cargo."
	cases := map[string]string{
		"bom":       "\ufeff" + sig + "\n" + cmt + "\n",
		"nul":       sig + "\x00\n" + cmt + "\n",
		"cr-only":   sig + "\r" + cmt + "\r",
		"trailing":  sig + " \n" + cmt + "\n",
		"one-line":  sig + "\n",
		"swapped":   cmt + "\n" + sig + "\n",
		"blank-mid": sig + "\n\n" + cmt + "\n",
		"lower":     "signature: 8a477f597d28d172789f06886806bc55\n" + cmt + "\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "x")
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), content)
			writeRegular(t, filepath.Join(dir, "debug", "f"))
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
}

// A8: HOME unset (cron/launchd/env -i). The home exclusions are silently
// skipped instead of failing closed, so a scan of the real home's parent
// admits the live default cache.
func TestRelocatedRegress_A8_HomeUnsetSkipsExclusion(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "users", "someone")
	live := relocatedGo(t, filepath.Join(home, "Library", "Caches", "go-build"))
	t.Setenv("GOCACHE", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	assertAbsent(t, r4bJobs(t, builtInConfig(filepath.Join(root, "users"))), live)
}
