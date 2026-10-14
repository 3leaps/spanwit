package envelope

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Disclosure path levels, ordered: none < analysis < system < full.
const (
	PathsNone     = "none"
	PathsAnalysis = "analysis"
	PathsSystem   = "system"
	PathsFull     = "full"
)

// Disclosure image-ref levels.
const (
	ImageRefsAggregate = "aggregate"
	ImageRefsLocal     = "local"
)

// Disclosure summarizes what a document discloses. It is derived from the
// emitted content, never from flags. A stored summary wider than the content
// is allowed; a narrower one fails to load.
type Disclosure struct {
	Paths           string `json:"paths"`
	ImageRefs       string `json:"image_refs"`
	ProcessIdentity bool   `json:"process_identity"`
}

// SystemMounts are the OS root mounts that identify nothing about the user.
// A mount string is system only when it is exactly one of these.
var SystemMounts = []string{"/", "/System/Volumes/Data", "/home"}

// CatalogPaths are fixed paths compiled into spanwit's catalogs. They
// disclose nothing about the user and classify as analysis.
var CatalogPaths = []string{"/System/Library/AssetsV2"}

// anchorPointers are the only fields where the home directory classifies as
// system: the pressure anchor.
var anchorPointers = map[string]bool{
	"/pressure/path":                   true,
	"/capacity_accounting/target/path": true,
}

// idKeys are schema identifiers (URLs), never scanned at any depth.
var idKeys = map[string]bool{"$schema": true, "$id": true}

// envelopeKeys are top-level envelope objects never scanned: the summary
// itself. tool is scanned like any content.
var envelopeKeys = map[string]bool{"disclosure": true}

var pathRank = map[string]int{PathsNone: 0, PathsAnalysis: 1, PathsSystem: 2, PathsFull: 3}

// Context is what classification needs beyond the document itself.
type Context struct {
	// Roots are analysis roots: a path equal to or under one is analysis.
	Roots []string
	// Home is the home directory accepted as the pressure anchor. Empty
	// means no anchor is accepted (every anchor string classifies as usual).
	Home string
}

// Home returns the running user's home directory, cleaned and absolute, or
// "" when it cannot be resolved.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	return filepath.Clean(home)
}

// Derive classifies a document's content. doc is the decoded JSON value.
func Derive(doc any, ctx Context) Disclosure {
	d := Disclosure{Paths: PathsNone, ImageRefs: ImageRefsAggregate}
	walk(doc, "", ctx, &d)
	return d
}

func raise(d *Disclosure, level string) {
	if pathRank[level] > pathRank[d.Paths] {
		d.Paths = level
	}
}

func walk(v any, ptr string, ctx Context, d *Disclosure) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if idKeys[k] || (ptr == "" && envelopeKeys[k]) {
				continue
			}
			// Map keys are content too (observe state keys volumes by id;
			// a key can carry a path).
			for _, p := range pathTokens(k) {
				raise(d, classify(p, "", ctx))
			}
			if arr, ok := child.([]any); ok && len(arr) > 0 {
				switch k {
				case "image_refs":
					d.ImageRefs = ImageRefsLocal
				case "holders":
					d.ProcessIdentity = true
				}
			}
			walk(child, ptr+"/"+k, ctx, d)
		}
	case []any:
		for _, child := range x {
			walk(child, ptr+"/*", ctx, d)
		}
	case string:
		for _, p := range pathTokens(x) {
			raise(d, classify(p, ptr, ctx))
		}
	}
}

// classify returns the level one path string requires at this location.
func classify(p, ptr string, ctx Context) string {
	// Only a plain absolute path can be analysis or system; every other
	// token shape (home, env, dot-relative, encoded, file:, Windows) is full.
	if !strings.HasPrefix(p, "/") || hasDotDot(p) {
		return PathsFull
	}
	c := filepath.Clean(p)
	for _, r := range ctx.Roots {
		if r == "" {
			continue
		}
		if within(c, filepath.Clean(r)) {
			return PathsAnalysis
		}
	}
	for _, k := range CatalogPaths {
		if c == k {
			return PathsAnalysis
		}
	}
	if ctx.Home != "" && anchorPointers[ptr] && c == filepath.Clean(ctx.Home) {
		return PathsSystem
	}
	for _, m := range SystemMounts {
		if c == m {
			return PathsSystem
		}
	}
	return PathsFull
}

// hasDotDot reports a ".." segment anywhere in p. Such a path is full even
// when it cleans to somewhere inside a root: the string discloses its route.
func hasDotDot(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// within reports whether p equals root or is under it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// pathTokens extracts path-shaped tokens from a string value. A token
// starts at a position not preceded by a word character and is:
//   - absolute ("/x"), home-relative ("~", "~/x", "~user/x"), env-rooted
//     ("$HOME/x", "${HOME}/x"), dot-relative ("./x", "../x", "a/../b"),
//     a Windows drive, UNC or device path, or a file: URL;
//   - or any word containing a percent-encoded slash ("%2F").
//
// A whole value of "/" is the root mount; inside prose a lone "/" is not a
// path. http(s) URLs are not paths. Bare relative words ("registry/git")
// are indistinguishable from prose and are not tokens.
func pathTokens(s string) []string {
	if s == "/" {
		return []string{"/"}
	}
	var out []string
	for i := 0; i < len(s); {
		if i > 0 && wordByte(s[i-1]) {
			i++
			continue
		}
		j := i
		for j < len(s) && !terminator(s[j]) {
			j++
		}
		w := s[i:j]
		if j == i {
			i++
			continue
		}
		if w[0] == '*' {
			// A glob ("**/target"): never re-entered mid-word, so its "/x"
			// tail is not read as an absolute path. A built-in signature
			// pattern is fixed vocabulary; any other glob naming a location
			// is a relative path.
			if g := strings.TrimPrefix(strings.TrimRight(w, ".,;:)"), "pattern:"); !builtinPatterns[g] &&
				strings.ContainsAny(g, "/\\") {
				out = append(out, g)
			}
			i = j
			continue
		}
		if tok, ok := pathWord(w); ok {
			out = append(out, tok)
			i = j
			continue
		}
		if isWebURL(w) {
			// The URL itself is not a path, but a query or fragment value can
			// carry one ("?p=/etc/x"): scan what follows each '?', '&', '=' or '#'.
			for k := strings.IndexAny(w, "?#"); k >= 0 && k < len(w); k++ {
				if c := w[k]; c == '?' || c == '&' || c == '=' || c == '#' {
					if tok, ok := pathWord(w[k+1:]); ok {
						out = append(out, tok)
					}
				}
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// pathWord reports whether a word starting at a boundary is a path token.
func pathWord(w string) (string, bool) {
	lower := strings.ToLower(w)
	switch {
	case isWebURL(w):
		return "", false
	case strings.Contains(lower, "%2f"), strings.Contains(lower, "%5c"):
		return w, true
	case strings.HasPrefix(lower, "file:"):
		return w, true
	case windowsStart(w):
		return w, true
	case dotDotWord(w), w[0] == '.' && strings.ContainsAny(w, "/\\"):
		// "./x", "../x" and dotfile paths (".ssh/id_rsa") are paths.
		return w, true
	case envRooted(w):
		return w, true
	case w[0] == '~':
		if w == "~" || strings.HasPrefix(w, "~/") {
			return strings.TrimRight(w, ".:"), true
		}
		if k := strings.IndexByte(w, '/'); k > 1 && allWord(w[1:k]) {
			return w, true
		}
		return "", false
	case w[0] == '/':
		if len(w) == 1 || w[1] == '/' {
			return "", false
		}
		tok := strings.TrimRight(w, ".:")
		if tok == "/" || tok == "" {
			return "", false
		}
		return tok, true
	}
	return "", false
}

// isWebURL reports an http(s) URL word (schema ids, docs links).
func isWebURL(w string) bool {
	lower := strings.ToLower(w)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// envRooted reports "$NAME/..." or "${NAME}/..." (or a Windows %NAME%\...).
func envRooted(w string) bool {
	if strings.HasPrefix(w, "${") {
		k := strings.IndexByte(w, '}')
		return k > 2 && k+1 < len(w) && (w[k+1] == '/' || w[k+1] == '\\')
	}
	if w[0] == '$' {
		k := 1
		for k < len(w) && (isLetter(w[k]) || w[k] == '_' || (k > 1 && w[k] >= '0' && w[k] <= '9')) {
			k++
		}
		return k > 1 && k < len(w) && (w[k] == '/' || w[k] == '\\')
	}
	if w[0] == '%' {
		k := strings.IndexByte(w[1:], '%')
		return k > 0 && k+2 < len(w) && (w[k+2] == '\\' || w[k+2] == '/')
	}
	return false
}

func allWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if !wordByte(s[i]) {
			return false
		}
	}
	return s != ""
}

// dotDotWord reports a word with a parent segment ("..", "../x", "a/../b").
func dotDotWord(w string) bool {
	return hasDotDot(w)
}

// windowsStart reports a drive path ("C:\\", "C:/"), or a UNC or device
// path ("\\\\").
func windowsStart(w string) bool {
	if len(w) > 2 && isLetter(w[0]) && w[1] == ':' && (w[2] == '\\' || w[2] == '/') {
		return true
	}
	return len(w) > 1 && w[0] == '\\' && w[1] == '\\'
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// wordByte reports characters that continue a word: a path token never
// starts right after one ("registry/git", "dry-run/--execute").
func wordByte(b byte) bool {
	return isLetter(b) || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '.'
}

func terminator(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\'', '"', '`', ')', ']', '}', '>', ',', ';':
		return true
	}
	return false
}

// Covers reports whether a stored summary is at least as wide as content.
func Covers(stored, content Disclosure) bool {
	if pathRank[stored.Paths] < pathRank[content.Paths] {
		return false
	}
	if content.ImageRefs == ImageRefsLocal && stored.ImageRefs != ImageRefsLocal {
		return false
	}
	if content.ProcessIdentity && !stored.ProcessIdentity {
		return false
	}
	return true
}

// Valid reports whether every field holds a known value.
func (d Disclosure) Valid() bool {
	_, ok := pathRank[d.Paths]
	return ok && (d.ImageRefs == ImageRefsAggregate || d.ImageRefs == ImageRefsLocal)
}

// Stamp derives the summary for a struct about to be written, using the
// running user's home as the accepted anchor.
func Stamp(v any, roots ...string) (*Disclosure, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	d := Derive(doc, Context{Roots: roots, Home: Home()})
	return &d, nil
}

// Checked is the outcome of reading a document's envelope at load time.
type Checked struct {
	Tool       Tool
	Disclosure Disclosure
	// Legacy is true when the document carried no disclosure summary.
	Legacy   bool
	Warnings []string
}

// Check reads the envelope of a decoded document. A document without a
// summary takes legacy (the most disclosed level for its kind). A stored
// summary narrower than the content fails, except when the only gap is a
// pressure anchor that is not this user's home: then it is widened to the
// content with a warning, so a capture moved between accounts still loads.
func Check(raw []byte, roots []string, legacy Disclosure) (Checked, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Checked{}, fmt.Errorf("parse envelope: %w", err)
	}
	out := Checked{Tool: UnknownTool}
	if t, ok := doc["tool"].(map[string]any); ok {
		name, _ := t["name"].(string)
		version, _ := t["version"].(string)
		commit, _ := t["commit"].(string)
		if name == "" || version == "" {
			return Checked{}, fmt.Errorf("envelope tool requires name and version")
		}
		out.Tool = Tool{Name: name, Version: version, Commit: commit}
	}
	sd, ok := doc["disclosure"]
	if !ok {
		out.Legacy = true
		out.Disclosure = legacy
		return out, nil
	}
	b, err := json.Marshal(sd)
	if err != nil {
		return Checked{}, err
	}
	var stored Disclosure
	if err := json.Unmarshal(b, &stored); err != nil || !stored.Valid() {
		return Checked{}, fmt.Errorf("envelope disclosure is malformed")
	}
	content := Derive(doc, Context{Roots: roots, Home: Home()})
	if Covers(stored, content) {
		out.Disclosure = stored
		return out, nil
	}
	// Retry trusting the document's own anchor. If the stored summary covers
	// that, the only gap is the anchor: widen, never fail.
	claimed := Derive(doc, Context{Roots: roots, Home: anchorOf(doc)})
	if Covers(stored, claimed) {
		out.Disclosure = content
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"disclosure widened from paths=%s to paths=%s: the pressure anchor is not this user's home directory",
			stored.Paths, content.Paths))
		return out, nil
	}
	return Checked{}, fmt.Errorf(
		"envelope disclosure understates content: stored paths=%s image_refs=%s process_identity=%t, content paths=%s image_refs=%s process_identity=%t",
		stored.Paths, stored.ImageRefs, stored.ProcessIdentity, content.Paths, content.ImageRefs, content.ProcessIdentity)
}

func anchorOf(doc map[string]any) string {
	if p, ok := doc["pressure"].(map[string]any); ok {
		if s, ok := p["path"].(string); ok {
			return s
		}
	}
	if ca, ok := doc["capacity_accounting"].(map[string]any); ok {
		if t, ok := ca["target"].(map[string]any); ok {
			if s, ok := t["path"].(string); ok {
				return s
			}
		}
	}
	return ""
}
