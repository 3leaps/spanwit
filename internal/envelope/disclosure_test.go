package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPathTokens(t *testing.T) {
	cases := map[string][]string{
		"/":                                     {"/"},
		"/fixture/usr-a":                        {"/fixture/usr-a"},
		"spanwit space '/private/var/T'":        {"/private/var/T"},
		"see https://schemas.3leaps.dev/x.json": nil,
		"left=1 right=2":                        nil,
		"a/b relative":                          nil,
		"~/Library/Caches":                      {"~/Library/Caches"},
		"~":                                     {"~"},
		"size 3/4 done":                         nil,
		"path=/tmp/x.":                          {"/tmp/x"},
		`D:\fixture\a\data`:                     {`D:\fixture\a\data`},
		`\\server\share\x`:                      {`\\server\share\x`},
		"either / or":                           nil,
	}
	for in, want := range cases {
		got := pathTokens(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("pathTokens(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDeriveLevels(t *testing.T) {
	ctx := Context{Roots: []string{"/an"}, Home: "/fixture/usr-a"}
	cases := []struct {
		name, doc, want string
	}{
		{"no paths", `{"volumes":[{"volume_id":"v","label":"Data"}]}`, PathsNone},
		{"root and under", `{"root":"/an","x":["/an/b/c"]}`, PathsAnalysis},
		{"catalog constant", `{"root":"/an","p":"/System/Library/AssetsV2"}`, PathsAnalysis},
		{"home as anchor", `{"pressure":{"path":"/fixture/usr-a","mount":"/System/Volumes/Data"}}`, PathsSystem},
		{"home as target anchor", `{"capacity_accounting":{"target":{"path":"/fixture/usr-a","mount":"/"}}}`, PathsSystem},
		{"home outside anchor", `{"hotspots":[{"path":"/fixture/usr-a"}]}`, PathsFull},
		{"under home", `{"pressure":{"path":"/fixture/usr-a/x"}}`, PathsFull},
		{"other mount", `{"pressure":{"mount":"/Volumes/Ext"}}`, PathsFull},
		{"home-shaped mount", `{"pressure":{"mount":"/home/fixture-user"}}`, PathsFull},
		{"linux home mount", `{"pressure":{"mount":"/home"}}`, PathsSystem},
		{"temp root", `{"temp_plane_coverage":{"planes":[{"root":"/private/var/folders/x/T"}]}}`, PathsFull},
		{"path in prose", `{"warnings":["could not read /etc/x"]}`, PathsFull},
		{"tilde", `{"keep":["~/.cargo"]}`, PathsFull},
		{"dotdot escapes root", `{"x":"/an/../etc"}`, PathsFull},
		{"prefix is not containment", `{"x":"/anx/y"}`, PathsFull},
		{"schema url ignored", `{"$schema":"https://schemas.3leaps.dev/spanwit/x/v1.json"}`, PathsNone},
	}
	for _, c := range cases {
		if got := Derive(decode(t, c.doc), ctx).Paths; got != c.want {
			t.Errorf("%s: paths = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDeriveRefsAndProcessIdentity(t *testing.T) {
	ctx := Context{}
	d := Derive(decode(t, `{"container_runtime":{"image_refs":[]}}`), ctx)
	if d.ImageRefs != ImageRefsAggregate {
		t.Fatalf("an empty image_refs list is aggregate, got %s", d.ImageRefs)
	}
	d = Derive(decode(t, `{"container_runtime":{"image_refs":[{"ref":"r"}]}}`), ctx)
	if d.ImageRefs != ImageRefsLocal {
		t.Fatalf("a present image ref is local, got %s", d.ImageRefs)
	}
	d = Derive(decode(t, `{"held_open":{"entries":[{"holders":[{"pid":1,"uid":501,"executable":"x"}]}]}}`), ctx)
	if !d.ProcessIdentity {
		t.Fatal("any held-open holder sets process_identity")
	}
}

// The disclosure summary is not content; the tool object is scanned like
// any other value, so a path smuggled into it is caught.
func TestDeriveScansToolNotSummary(t *testing.T) {
	d := Derive(decode(t, `{"disclosure":{"paths":"/y"}}`), Context{})
	if d.Paths != PathsNone {
		t.Fatalf("the disclosure summary must not be scanned, got %s", d.Paths)
	}
	for _, field := range []string{"name", "version", "commit"} {
		doc := `{"tool":{"name":"spanwit","version":"1","commit":"abc"}}`
		doc = strings.Replace(doc, `"`+field+`":"`, `"`+field+`":"/fixture/usr-a/.ssh `, 1)
		if got := Derive(decode(t, doc), Context{}).Paths; got != PathsFull {
			t.Errorf("a path in tool.%s must derive full, got %s", field, got)
		}
	}
}

// A relative path with a parent segment can reach anywhere: it is a path
// token and always full, even when it would clean to inside a root.
func TestDeriveDotDotIsFull(t *testing.T) {
	ctx := Context{Roots: []string{"/an"}}
	for _, v := range []string{
		"see ../.ssh/id_rsa",
		"../x",
		"a/../b",
		`..\x`,
		"/an/b/../c",
		"..",
	} {
		if got := Derive(map[string]any{"w": v}, ctx).Paths; got != PathsFull {
			t.Errorf("%q must derive full, got %s", v, got)
		}
	}
	for _, v := range []string{"wait...", "a.. b", "x..y"} {
		if got := Derive(map[string]any{"w": v}, ctx).Paths; got != PathsNone {
			t.Errorf("%q is not a path, got %s", v, got)
		}
	}
}

// Path shapes that are not a plain absolute path at a word boundary still
// classify full; prose word pairs and web URLs are not paths.
func TestDeriveTokenShapes(t *testing.T) {
	ctx := Context{Roots: []string{"/an"}}
	full := []string{
		`{"volumes":{"/fixture/usr-a/x":{"id":"v"}}}`,
		`{"w":"file:///fixture/usr-a/x"}`,
		`{"w":"%2Ffixture%2Fusr-a"}`,
		`{"w":"path:/etc/x"}`,
		`{"w":"(/etc/x)"}`,
		`{"w":"a|/etc/x"}`,
		`{"w":"./secret/key"}`,
		`{"w":"~svc/x"}`,
		`{"w":"$HOME/.ssh"}`,
		`{"w":"${HOME}/.ssh"}`,
		`{"w":"%APPDATA%\\x"}`,
		`{"w":"\\\\?\\C:\\x"}`,
		`{"w":"https://example.test/?p=/etc/x"}`,
		`{"w":"https://example.test/#/fixture/usr-a"}`,
	}
	for _, doc := range full {
		if got := Derive(decode(t, doc), ctx).Paths; got != PathsFull {
			t.Errorf("%s must derive full, got %s", doc, got)
		}
	}
	none := []string{
		`{"w":"registry/git (GOPATH/pkg)"}`,
		`{"w":"dry-run/--execute macOS/Linux; incomplete/unknown"}`,
		`{"w":"see https://schemas.3leaps.dev/spanwit/x/v1.json"}`,
		`{"w":"3/4 done, wait..."}`,
		`{"w":"~ approx"}`,
	}
	for _, doc := range none {
		got := Derive(decode(t, doc), ctx).Paths
		if doc == `{"w":"~ approx"}` {
			if got != PathsFull {
				t.Errorf("a lone ~ is the home directory: %s got %s", doc, got)
			}
			continue
		}
		if got != PathsNone {
			t.Errorf("%s is prose, got %s", doc, got)
		}
	}
}

// Only a plain absolute path may classify below full. A file: URL, a
// dot-relative or an env-rooted token naming the analysis root (or a
// Windows path naming it) is full: it is not the root string itself.
func TestDeriveOnlyPlainAbsoluteBelowFull(t *testing.T) {
	ctx := Context{Roots: []string{"an"}}
	for _, v := range []string{"file:an/x", "./an/x", "$ROOT/an"} {
		if got := Derive(map[string]any{"w": v}, ctx).Paths; got != PathsFull {
			t.Errorf("%q must derive full even though it cleans under root %q, got %s", v, "an", got)
		}
	}
}

// secrev frqtfm6o / o1d4379i: each of these, as the only path in a
// document stored paths none, fails to load.
func TestCheckFiveEscapesFail(t *testing.T) {
	docs := []string{
		`{"tool":{"name":"spanwit","version":"1","commit":"/fixture/usr-a/.ssh"}}`,
		`{"notes":["see ../.ssh/id_rsa"]}`,
		`{"notes":["see .ssh/id_rsa"]}`,
		`{"notes":["file:///fixture/usr-a/secret"]}`,
		`{"notes":["~alice/notes"]}`,
	}
	for _, d := range docs {
		doc := strings.Replace(d, "{", `{"disclosure":{"paths":"none","image_refs":"aggregate","process_identity":false},`, 1)
		if _, err := Check([]byte(doc), nil, legacyFull()); err == nil {
			t.Errorf("must fail to load under paths none: %s", d)
		}
	}
}

// A dotfile-relative word (".ssh/id_rsa", ".config\\x") is a path and full.
// Undotted bare words stay non-tokens (the documented limit).
func TestDeriveDotfileRelative(t *testing.T) {
	for _, v := range []string{"see .ssh/id_rsa", ".config/gh/hosts.yml", `.aws\\credentials`} {
		if got := Derive(map[string]any{"w": v}, Context{}).Paths; got != PathsFull {
			t.Errorf("%q must derive full, got %s", v, got)
		}
	}
	for _, v := range []string{"etc/passwd", "registry/git", "macOS/Linux", "dry-run/--execute", ".hidden", "v1.2/3"} {
		if got := Derive(map[string]any{"w": v}, Context{}).Paths; got != PathsNone {
			t.Errorf("%q is a documented non-token, got %s", v, got)
		}
	}
}

// A built-in signature glob is fixed vocabulary; its "/x" tail is never read
// as an absolute path. Any other glob that names a location is full, and a
// glob does not hide an absolute path after it.
func TestDeriveGlobWords(t *testing.T) {
	ctx := Context{Roots: []string{"/an"}}
	for _, v := range []string{"**/target", "pattern:**/.gocache", "**/grype/db/grype-db-download*"} {
		if got := Derive(map[string]any{"pattern": v}, ctx).Paths; got != PathsNone {
			t.Errorf("built-in glob %q must be none, got %s", v, got)
		}
	}
	for _, v := range []string{"**/clients/acme", "pattern:*/fixture/usr-a/x", "*/etc/x"} {
		if got := Derive(map[string]any{"pattern": v}, ctx).Paths; got != PathsFull {
			t.Errorf("glob %q names a location and must be full, got %s", v, got)
		}
	}
	if got := Derive(map[string]any{"w": "**/target /etc/x"}, ctx).Paths; got != PathsFull {
		t.Errorf("a glob must not hide a following absolute path, got %s", got)
	}
}

func legacyFull() Disclosure {
	return Disclosure{Paths: PathsFull, ImageRefs: ImageRefsLocal, ProcessIdentity: true}
}

func TestCheckLegacyAndTool(t *testing.T) {
	c, err := Check([]byte(`{"root":"/an"}`), []string{"/an"}, legacyFull())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Legacy || c.Disclosure != legacyFull() || c.Tool != UnknownTool {
		t.Fatalf("legacy doc must load as the most disclosed level and tool unknown, got %+v", c)
	}
	if _, err := Check([]byte(`{"tool":{"name":"spanwit"}}`), nil, legacyFull()); err == nil {
		t.Fatal("a tool object without a version must fail")
	}
}

func TestCheckNarrowFails(t *testing.T) {
	cases := map[string]string{
		"paths":            `{"disclosure":{"paths":"analysis","image_refs":"aggregate","process_identity":false},"root":"/an","x":"/etc/y"}`,
		"image_refs":       `{"disclosure":{"paths":"none","image_refs":"aggregate","process_identity":false},"image_refs":[{"ref":"r"}]}`,
		"process_identity": `{"disclosure":{"paths":"none","image_refs":"aggregate","process_identity":false},"e":[{"holders":[{"pid":1}]}]}`,
		"malformed":        `{"disclosure":{"paths":"some","image_refs":"aggregate","process_identity":false}}`,
	}
	for name, doc := range cases {
		if _, err := Check([]byte(doc), []string{"/an"}, legacyFull()); err == nil {
			t.Errorf("%s: a narrower summary must fail to load", name)
		}
	}
	// Wider is allowed.
	if _, err := Check([]byte(`{"disclosure":{"paths":"full","image_refs":"local","process_identity":true},"root":"/an"}`), []string{"/an"}, legacyFull()); err != nil {
		t.Fatalf("a wider summary must load: %v", err)
	}
}

// A system summary whose pressure anchor is not this user's home widens to
// full with a warning instead of failing; a non-home anchor string that also
// appears elsewhere still classifies full there.
func TestCheckForeignAnchorWidens(t *testing.T) {
	doc := `{"disclosure":{"paths":"system","image_refs":"aggregate","process_identity":false},` +
		`"root":"/an","pressure":{"path":"/nonexistent-home-zz","mount":"/"}}`
	c, err := Check([]byte(doc), []string{"/an"}, legacyFull())
	if err != nil {
		t.Fatalf("a foreign anchor must widen, not fail: %v", err)
	}
	if c.Disclosure.Paths != PathsFull || len(c.Warnings) != 1 {
		t.Fatalf("want widened to full with one warning, got %+v", c)
	}
	// secrev T1/T2: a path in tool or a ".." route under paths none fails.
	for _, doc := range []string{
		`{"disclosure":{"paths":"none","image_refs":"aggregate","process_identity":false},"tool":{"name":"spanwit","version":"1","commit":"/fixture/usr-a/.ssh"}}`,
		`{"disclosure":{"paths":"none","image_refs":"aggregate","process_identity":false},"notes":["see ../.ssh/id_rsa"]}`,
	} {
		if _, err := Check([]byte(doc), nil, legacyFull()); err == nil {
			t.Errorf("must fail to load: %s", doc)
		}
	}
	// The same string as a hotspot is not an anchor: the summary fails.
	doc = `{"disclosure":{"paths":"system","image_refs":"aggregate","process_identity":false},` +
		`"root":"/an","pressure":{"path":"/etc","mount":"/"},"hotspots":[{"path":"/etc"}]}`
	if _, err := Check([]byte(doc), []string{"/an"}, legacyFull()); err == nil {
		t.Fatal("a claimed anchor must not cover the same string outside the anchor fields")
	}
}
