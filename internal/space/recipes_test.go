package space

import (
	"context"
	"strings"
	"testing"
)

func TestParseRustupToolchainDir(t *testing.T) {
	cases := map[string]struct {
		channel string
		valid   bool
	}{
		"stable-aarch64-apple-darwin":                 {channel: "stable", valid: true},
		"nightly-aarch64-apple-darwin":                {channel: "nightly", valid: true},
		"nightly-2023-10-05-x86_64-unknown-linux-gnu": {channel: "nightly-2023-10-05", valid: true},
		"1.89-aarch64-apple-darwin":                   {channel: "1.89", valid: true},
		"1.89.0-aarch64-apple-darwin":                 {channel: "1.89.0", valid: true},
		"1.88.0-x86_64-unknown-linux-gnu":             {channel: "1.88.0", valid: true},
		"1.89;echo_PWNED-aarch64-apple-darwin":        {valid: false},
		"1.89$(id)-aarch64-apple-darwin":              {valid: false},
		"":                                            {valid: false},
	}
	for in, want := range cases {
		got := parseRustupToolchainDir(in)
		if got.Valid != want.valid {
			t.Errorf("parse(%q).Valid=%v want %v", in, got.Valid, want.valid)
			continue
		}
		if want.valid && got.Channel != want.channel {
			t.Errorf("parse(%q).Channel=%q want %q", in, got.Channel, want.channel)
		}
	}
}

func TestRustupRecipe_NearDupe(t *testing.T) {
	dirs := []string{
		"stable-aarch64-apple-darwin",
		"nightly-aarch64-apple-darwin",
		"1.89-aarch64-apple-darwin",
		"1.89.0-aarch64-apple-darwin",
		"1.88.0-aarch64-apple-darwin",
	}
	r, ok := rustupRecipe(context.Background(), RecipeOptions{
		IncludeRustup: true,
		Home:          "/tmp/home",
		ToolchainDirs: dirs,
	})
	if !ok {
		t.Fatal("expected recipe")
	}
	if r.State != StateDiagnosticOnly {
		t.Fatalf("recipe state must be diagnostic-only, got %q", r.State)
	}
	keep := strings.Join(r.Keep, ",")
	if !strings.Contains(keep, "stable") || !strings.Contains(keep, "nightly") {
		t.Fatalf("keep missing channels: %v", r.Keep)
	}
	if !strings.Contains(keep, "1.89.0") {
		t.Fatalf("keep should prefer 1.89.0: %v", r.Keep)
	}
	if strings.Contains(","+keep+",", ",1.89,") {
		t.Fatalf("keep should not include bare 1.89 when 1.89.0 present: %v", r.Keep)
	}
	var uninstalls []string
	for _, c := range r.SuggestedCommands {
		if len(c.Args) >= 3 && c.Args[0] == "toolchain" && c.Args[1] == "uninstall" {
			uninstalls = append(uninstalls, c.Args[2])
			// argv must be safe tokens only
			if !IsSafeToken(c.Args[2]) {
				t.Fatalf("unsafe uninstall arg: %q", c.Args[2])
			}
		}
	}
	joined := strings.Join(uninstalls, ",")
	if !strings.Contains(joined, "1.89") || strings.Contains(joined, "1.89.0") {
		t.Fatalf("uninstall set: %v", uninstalls)
	}
	for _, c := range r.SuggestedCommands {
		if strings.Contains(c.Display, "spanwit prune") {
			t.Fatalf("recipe must not suggest prune: %s", c.Display)
		}
	}
}

func TestRustupRecipe_RejectsInjectionAndKeepContradiction(t *testing.T) {
	dirs := []string{
		"1.89.0-aarch64-apple-darwin",
		"1.89.0-x86_64-unknown-linux-gnu", // multi-host same channel — both kept
		"1.89;echo_PWNED-aarch64-apple-darwin",
	}
	r, ok := rustupRecipe(context.Background(), RecipeOptions{
		Home:          "/tmp/evil",
		ToolchainDirs: dirs,
	})
	if !ok {
		t.Fatal("expected recipe from valid pins")
	}
	// Keep 1.89.0; must NOT suggest uninstall 1.89.0 (multi-host keep).
	for _, c := range r.SuggestedCommands {
		for _, a := range c.Args {
			if a == "1.89.0" && containsArg(c.Args, "uninstall") {
				t.Fatalf("must not uninstall kept channel 1.89.0: %#v", c)
			}
			if strings.Contains(a, "PWNED") || strings.Contains(a, ";") {
				t.Fatalf("injection token leaked into argv: %#v", c)
			}
		}
		if strings.Contains(c.Display, "PWNED") || strings.Contains(c.Display, ";echo") {
			t.Fatalf("injection in display: %s", c.Display)
		}
	}
	keep := strings.Join(r.Keep, ",")
	if !strings.Contains(keep, "1.89.0") {
		t.Fatalf("keep: %v", r.Keep)
	}
}

func TestRustupRecipe_DatedNightlyPreserved(t *testing.T) {
	dirs := []string{
		"nightly-2023-10-05-aarch64-apple-darwin",
		"stable-aarch64-apple-darwin",
	}
	r, ok := rustupRecipe(context.Background(), RecipeOptions{
		Home:          "/tmp/h",
		ToolchainDirs: dirs,
	})
	if !ok {
		t.Fatal("expected recipe")
	}
	keep := strings.Join(r.Keep, ",")
	if !strings.Contains(keep, "nightly-2023-10-05") {
		t.Fatalf("dated nightly truncated/lost: %v", r.Keep)
	}
	// Must not keep only bare "nightly-2023"
	if strings.Contains(","+keep+",", ",nightly-2023,") {
		t.Fatalf("nightly truncated to year: %v", r.Keep)
	}
}

func TestRustupRecipe_PrereleaseLosesToStablePatch(t *testing.T) {
	dirs := []string{
		"1.89.0-beta.1-aarch64-apple-darwin",
		"1.89.0-aarch64-apple-darwin",
	}
	// Note: 1.89.0-beta.1-aarch64... — parse splits at first arch token.
	// Dir "1.89.0-beta.1-aarch64-apple-darwin" → channel 1.89.0-beta.1
	r, ok := rustupRecipe(context.Background(), RecipeOptions{
		Home:          "/tmp/h",
		ToolchainDirs: dirs,
	})
	if !ok {
		t.Fatal("expected recipe")
	}
	keep := strings.Join(r.Keep, ",")
	if !strings.Contains(keep, "1.89.0") {
		t.Fatalf("keep: %v", r.Keep)
	}
	// Prefer final patch over prerelease in keep when both present.
	var uninstalls []string
	for _, c := range r.SuggestedCommands {
		if containsArg(c.Args, "uninstall") {
			uninstalls = append(uninstalls, c.Args[len(c.Args)-1])
		}
	}
	// If prerelease is distinct near-dupe, it may be suggested for uninstall.
	for _, u := range uninstalls {
		if u == "1.89.0" {
			t.Fatalf("must not uninstall preferred patch: %v", uninstalls)
		}
	}
}

func TestBuildRecipes_EmptyWhenNoRustup(t *testing.T) {
	recipes := BuildRecipes(context.Background(), RecipeOptions{
		IncludeRustup: true,
		Home:          t.TempDir(),
	})
	if len(recipes) != 0 {
		t.Fatalf("expected no recipes, got %#v", recipes)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
