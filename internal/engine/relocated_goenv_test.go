package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// goEnvCase builds a relocated Go cache outside any live location, points
// GOENV per setup, and reports whether discovery and exact replay keep the
// cache out of the prunable set.
func goEnvCase(t *testing.T, setup func(t *testing.T, base, cache string)) (discovered, replayed bool) {
	t.Helper()
	isolateLiveCaches(t)
	base := t.TempDir()
	root := filepath.Join(base, "scan")
	cache := relocatedGo(t, filepath.Join(root, "old-cache"))
	ageTree(t, cache, 48*time.Hour)
	setup(t, base, cache)

	discovered = false
	for _, c := range r4bJobs(t, builtInConfig(root)) {
		if c.Path == cache && c.State == CandidateStatePrunable {
			discovered = true
		}
	}
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		RequestedMinAge: RequestedMinAgeNone,
		AnalysisRoot:    root,
		Entries: []ExactEntry{{Path: cache, Signature: relocatedGoID,
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}},
	})
	replayed = false
	if err == nil {
		for _, c := range plan.Candidates {
			if c.Path == cache && c.State == CandidateStatePrunable {
				replayed = true
			}
		}
	}
	return discovered, replayed
}

func writeEnvFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Negative controls: GOENV names the cache (or cannot be read completely),
// so the cache is never prunable on either path.
func TestGoEnv_UncertainOrNamedCacheNeverPrunable(t *testing.T) {
	cases := map[string]func(t *testing.T, base, cache string){
		"short regular names cache": func(t *testing.T, base, cache string) {
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "GOCACHE="+cache+"\n")
			t.Setenv("GOENV", f)
		},
		"symlinked env file": func(t *testing.T, base, cache string) {
			if runtime.GOOS == "windows" {
				t.Skip("unix symlink")
			}
			real := filepath.Join(base, "real-env")
			writeEnvFile(t, real, "GOCACHE="+cache+"\n")
			link := filepath.Join(base, "env-link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOENV", link)
		},
		"dangling link": func(t *testing.T, base, _ string) {
			if runtime.GOOS == "windows" {
				t.Skip("unix symlink")
			}
			link := filepath.Join(base, "env-link")
			if err := os.Symlink(filepath.Join(base, "missing"), link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOENV", link)
		},
		"over limit, cache named after the cap": func(t *testing.T, base, cache string) {
			f := filepath.Join(base, "env")
			pad := strings.Repeat("# padding line for the go env file\n", goEnvFileLimit/35+10)
			writeEnvFile(t, f, pad+"GOCACHE="+cache+"\n")
			t.Setenv("GOENV", f)
		},
		"unreadable env file": func(t *testing.T, base, cache string) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("unix permissions as non-root")
			}
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "GOCACHE="+cache+"\n")
			if err := os.Chmod(f, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f, 0o600) })
			t.Setenv("GOENV", f)
		},
		"env file is a directory": func(t *testing.T, base, _ string) {
			d := filepath.Join(base, "envdir")
			mkdirs(t, d)
			t.Setenv("GOENV", d)
		},
		"relative GOENV": func(t *testing.T, _, _ string) {
			t.Setenv("GOENV", "relative/env")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d, r := goEnvCase(t, setup)
			if d || r {
				t.Fatalf("cache must not be prunable: discovery=%v replay=%v", d, r)
			}
		})
	}
}

// Positive controls: a known-absent, off, or complete env file that names
// another cache leaves the scratch cache reclaimable on both paths.
func TestGoEnv_CertainConfigurationsKeepScratchCachePrunable(t *testing.T) {
	cases := map[string]func(t *testing.T, base, cache string){
		"GOENV off": func(t *testing.T, _, _ string) { t.Setenv("GOENV", "off") },
		"GOENV absent file": func(t *testing.T, base, _ string) {
			t.Setenv("GOENV", filepath.Join(base, "no-such-env"))
		},
		"complete file naming another cache": func(t *testing.T, base, _ string) {
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "GOFLAGS=-mod=mod\nGOCACHE="+filepath.Join(base, "elsewhere")+"\n")
			t.Setenv("GOENV", f)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d, r := goEnvCase(t, setup)
			if !d || !r {
				t.Fatalf("scratch cache should be prunable: discovery=%v replay=%v", d, r)
			}
		})
	}
}

// The env file value is verbatim: a cache path ending in a space is the
// live cache under exactly that name.
func TestGoEnv_SpaceEndingCachePathExcluded(t *testing.T) {
	isolateLiveCaches(t)
	base := t.TempDir()
	root := filepath.Join(base, "scan")
	cache := relocatedGo(t, filepath.Join(root, "old-cache "))
	ageTree(t, cache, 48*time.Hour)
	f := filepath.Join(base, "env")
	writeEnvFile(t, f, "GOCACHE="+cache+"\n")
	t.Setenv("GOENV", f)
	for _, c := range r4bJobs(t, builtInConfig(root)) {
		if c.Path == cache && c.State == CandidateStatePrunable {
			t.Fatalf("space-ending live cache must not be prunable on discovery")
		}
	}
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		RequestedMinAge: RequestedMinAgeNone, AnalysisRoot: root,
		Entries: []ExactEntry{{Path: cache, Signature: relocatedGoID,
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}},
	})
	if err == nil {
		for _, c := range plan.Candidates {
			if c.Path == cache && c.State == CandidateStatePrunable {
				t.Fatalf("space-ending live cache must not be prunable on replay")
			}
		}
	}
}

// Malformed keys are ignored exactly as the go command ignores them.
func TestGoEnv_IgnoredKeyForms(t *testing.T) {
	// The valid assignment names the scratch cache; a later ignored line
	// naming another path must not override it: never prunable.
	neg := func(prefix, suffix string) func(t *testing.T, base, cache string) {
		return func(t *testing.T, base, cache string) {
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "GOCACHE="+cache+"\n"+prefix+"GOCACHE"+suffix+"="+filepath.Join(base, "other")+"\n")
			t.Setenv("GOENV", f)
		}
	}
	for name, setup := range map[string]func(t *testing.T, base, cache string){
		"space-prefixed key after valid": neg(" ", ""),
		"space-suffixed key after valid": neg("", " "),
		"comment key after valid":        neg("#", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if d, r := goEnvCase(t, setup); d || r {
				t.Fatalf("cache must not be prunable: discovery=%v replay=%v", d, r)
			}
		})
	}
	// The valid assignment names another path; an ignored line naming the
	// scratch cache does not exclude it: prunable.
	pos := func(prefix, suffix string) func(t *testing.T, base, cache string) {
		return func(t *testing.T, base, cache string) {
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "GOCACHE="+filepath.Join(base, "other")+"\n"+prefix+"GOCACHE"+suffix+"="+cache+"\n")
			t.Setenv("GOENV", f)
		}
	}
	for name, setup := range map[string]func(t *testing.T, base, cache string){
		"space-prefixed key ignored": pos(" ", ""),
		"space-suffixed key ignored": pos("", " "),
		"lowercase key ignored": func(t *testing.T, base, cache string) {
			f := filepath.Join(base, "env")
			writeEnvFile(t, f, "gocache="+cache+"\n")
			t.Setenv("GOENV", f)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if d, r := goEnvCase(t, setup); !d || !r {
				t.Fatalf("cache should be prunable: discovery=%v replay=%v", d, r)
			}
		})
	}
}

func TestParseGoEnvFile_MatchesGoParser(t *testing.T) {
	cases := []struct{ data, want string }{
		{"GOCACHE=/a\n", "/a"},
		{"GOCACHE=/a \n", "/a "},
		{"GOCACHE= /a\n", " /a"},
		{"GOCACHE=/a\r\n", "/a\r"},
		{"GOCACHE=/a\nGOCACHE=/b", "/b"},
		{" GOCACHE=/a\n", ""},
		{"GOCACHE =/a\n", ""},
		{"#GOCACHE=/a\n", ""},
		{"GOCACHE\n", ""},
		{"\n\nGOCACHE=/a=b\n", "/a=b"},
	}
	for _, c := range cases {
		if got := parseGoEnvFile([]byte(c.data), "GOCACHE"); got != c.want {
			t.Errorf("parse %q = %q, want %q", c.data, got, c.want)
		}
	}
}
