package space

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Parsed toolchain directory name (rustup install layout).
type rustupToolchain struct {
	// Dir is the full directory basename under toolchains/.
	Dir string
	// Channel is the channel/version without host triple (e.g. 1.89.0, nightly-2023-10-05).
	Channel string
	// Host is the target triple suffix (may be empty if unparseable).
	Host string
	// Valid is false when the directory name failed strict validation.
	Valid bool
}

// rustupRecipe builds a guided recipe for the rustup toolchain stack.
// Policy:
//   - Keep: stable, nightly(+dated), beta, and version pins that are not near-duplicates
//   - Suggest uninstall of near-duplicates (e.g. 1.89 when 1.89.0 is present)
//   - Multi-host installs of the same channel are all kept (never uninstall keep channel)
//   - Invalid/malicious directory names are never emitted as command arguments
//   - Never auto-execute; never mark as prunable
func rustupRecipe(ctx context.Context, opts RecipeOptions) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	_, dirs, path, err := listRustupToolchainDirs(opts)
	if err != nil || len(dirs) == 0 {
		return Recipe{}, false
	}

	parsed := make([]rustupToolchain, 0, len(dirs))
	var rejected []string
	for _, d := range dirs {
		tc := parseRustupToolchainDir(d)
		if !tc.Valid {
			rejected = append(rejected, d)
			continue
		}
		parsed = append(parsed, tc)
	}
	if len(parsed) == 0 {
		return Recipe{}, false
	}

	alwaysKeepBase := map[string]bool{
		"stable":  true,
		"nightly": true,
		"beta":    true,
	}

	// Group by channel (host-independent). Multi-host copies of the same channel
	// count as one channel for near-dupe policy; all hosts of a kept channel stay.
	type channelGroup struct {
		channel string
		hosts   []string // full dir names for this channel
	}
	byChannel := map[string]*channelGroup{}
	var channelOrder []string
	for _, p := range parsed {
		g := byChannel[p.Channel]
		if g == nil {
			g = &channelGroup{channel: p.Channel}
			byChannel[p.Channel] = g
			channelOrder = append(channelOrder, p.Channel)
		}
		g.hosts = append(g.hosts, p.Dir)
	}

	// Partition into always-keep vs version-like.
	var keepChannels []string
	// version groups by major.minor
	type minorGroup struct {
		channels []string
	}
	byMinor := map[string]*minorGroup{}

	for _, ch := range channelOrder {
		base := channelBaseName(ch)
		if alwaysKeepBase[base] {
			keepChannels = append(keepChannels, ch)
			continue
		}
		minor, ok := versionMinorKey(ch)
		if !ok {
			// Unknown but valid form — keep (safe).
			keepChannels = append(keepChannels, ch)
			continue
		}
		g := byMinor[minor]
		if g == nil {
			g = &minorGroup{}
			byMinor[minor] = g
		}
		g.channels = append(g.channels, ch)
	}

	var suggestUninstall []string
	rationale := []string{
		"Rustup toolchains are diagnostic-only: spanwit never deletes them.",
		"Keep stable + nightly + required MSRV pins; drop near-duplicate channel aliases only.",
		"Commands use argv form; review before running. Multi-host installs of a kept channel are retained.",
	}
	for _, d := range rejected {
		rationale = append(rationale, fmt.Sprintf("ignored invalid toolchain directory name (not used in commands): %q", d))
	}

	for minor, g := range byMinor {
		// Deduplicate channel list within minor group.
		chans := uniqueSorted(append([]string(nil), g.channels...))
		if len(chans) == 1 {
			keepChannels = append(keepChannels, chans[0])
			continue
		}
		best := selectPreferredChannel(chans)
		keepChannels = append(keepChannels, best)
		for _, ch := range chans {
			if ch == best {
				continue
			}
			// Only suggest uninstall of the channel form (not host-specific dir)
			// when it is a strict-safe token. Never uninstall the keep channel.
			if ch == best {
				continue
			}
			if !IsSafeToken(ch) {
				rationale = append(rationale, fmt.Sprintf("skipped unsafe uninstall token for %s", minor))
				continue
			}
			suggestUninstall = append(suggestUninstall, ch)
			rationale = append(rationale, fmt.Sprintf(
				"near-duplicate pin for %s: keep %s, consider uninstalling %s",
				minor, best, ch))
		}
	}

	sort.Strings(keepChannels)
	keepChannels = uniqueSorted(keepChannels)
	// Ensure no suggestUninstall channel is also in keep set.
	keepSet := map[string]bool{}
	for _, k := range keepChannels {
		keepSet[k] = true
	}
	filteredUninstall := make([]string, 0, len(suggestUninstall))
	for _, u := range uniqueSorted(suggestUninstall) {
		if keepSet[u] {
			continue
		}
		if !IsSafeToken(u) {
			continue
		}
		filteredUninstall = append(filteredUninstall, u)
	}
	suggestUninstall = filteredUninstall

	cmds := make([]SuggestedCommand, 0, len(suggestUninstall)+2)
	cmds = append(cmds, FormatCommand("rustup", "toolchain", "list"))
	for _, ch := range suggestUninstall {
		cmds = append(cmds, FormatCommand("rustup", "toolchain", "uninstall", ch))
	}
	if len(suggestUninstall) == 0 {
		cmds = append(cmds, FormatCommand("rustup", "toolchain", "list", "-v"))
		rationale = append(rationale, "no near-duplicate channel pairs detected among installed pins")
	}

	r := Recipe{
		ID:                 "rustup-toolchains",
		Title:              "Rustup toolchain stack",
		State:              StateDiagnosticOnly,
		RebuildExpectation: RebuildLow,
		Path:               path,
		Keep:               keepChannels,
		SuggestedCommands:  cmds,
		Rationale:          rationale,
		CatalogID:          "development.rust.rustup",
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// parseRustupToolchainDir strictly parses a rustup toolchain directory basename.
// Invalid names (shell metacharacters, empty, unknown shape) are rejected.
func parseRustupToolchainDir(dir string) rustupToolchain {
	out := rustupToolchain{Dir: dir}
	if dir == "" || !IsSafeToken(dir) {
		return out
	}
	// Split host triple from the right using known arch tokens.
	parts := strings.Split(dir, "-")
	if len(parts) == 0 {
		return out
	}

	// Find first arch token index.
	archIdx := -1
	for i, p := range parts {
		if isArchToken(p) {
			archIdx = i
			break
		}
	}
	if archIdx <= 0 {
		// No host triple — allow bare channel names (stable, nightly, 1.89.0).
		if isValidChannel(dir) {
			out.Channel = dir
			out.Valid = true
		}
		return out
	}
	channel := strings.Join(parts[:archIdx], "-")
	host := strings.Join(parts[archIdx:], "-")
	if !isValidChannel(channel) {
		return out
	}
	// Host must be arch-vendor-os… with only safe tokens.
	for _, p := range parts[archIdx:] {
		if !IsSafeToken(p) {
			return out
		}
	}
	out.Channel = channel
	out.Host = host
	out.Valid = true
	return out
}

func isValidChannel(ch string) bool {
	if ch == "" || !IsSafeToken(ch) {
		return false
	}
	base := channelBaseName(ch)
	switch base {
	case "stable", "beta", "nightly":
		// nightly or nightly-YYYY-MM-DD
		if base == "nightly" && ch != "nightly" {
			return isDatedNightly(ch)
		}
		return ch == base || (base == "nightly" && isDatedNightly(ch))
	}
	// Version: 1.89, 1.89.0, 1.89.0-beta.1 (beta segment must be safe)
	return versionMinorKeyOK(ch)
}

func isDatedNightly(ch string) bool {
	// nightly-YYYY-MM-DD
	if !strings.HasPrefix(ch, "nightly-") {
		return false
	}
	rest := strings.TrimPrefix(ch, "nightly-")
	parts := strings.Split(rest, "-")
	if len(parts) != 3 {
		return false
	}
	if len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return false
	}
	for _, p := range parts {
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func channelBaseName(ch string) string {
	if ch == "stable" || ch == "beta" || ch == "nightly" {
		return ch
	}
	if strings.HasPrefix(ch, "nightly-") {
		return "nightly"
	}
	if strings.HasPrefix(ch, "stable-") {
		return "stable"
	}
	if strings.HasPrefix(ch, "beta-") {
		return "beta"
	}
	return ch
}

func versionMinorKeyOK(ch string) bool {
	_, ok := versionMinorKey(ch)
	return ok
}

// versionMinorKey returns "1.89" for "1.89", "1.89.0", or "1.89.0-beta.1".
// Prerelease suffixes after the patch are allowed only with safe tokens.
func versionMinorKey(ch string) (string, bool) {
	// Split prerelease: 1.89.0-beta.1 → core=1.89.0
	core := ch
	if i := strings.IndexByte(ch, '-'); i >= 0 {
		// version cores don't start with nightly etc.
		if ch[0] < '0' || ch[0] > '9' {
			return "", false
		}
		core = ch[:i]
		pre := ch[i+1:]
		if pre == "" || !IsSafeToken(strings.ReplaceAll(pre, ".", "-")) {
			// allow beta.1 by checking each segment
			for _, seg := range strings.Split(pre, ".") {
				if !IsSafeToken(seg) {
					return "", false
				}
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return "", false
	}
	for _, p := range parts {
		if p == "" {
			return "", false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return "", false
			}
		}
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", false
	}
	return fmt.Sprintf("%d.%d", maj, min), true
}

// selectPreferredChannel prefers stable patch forms over bare minor and over prereleases.
// Preference: 1.89.0 > 1.89 > 1.89.0-beta.1
func selectPreferredChannel(chans []string) string {
	best := chans[0]
	bestScore := channelSpecificity(best)
	for _, ch := range chans[1:] {
		s := channelSpecificity(ch)
		if s > bestScore || (s == bestScore && ch < best) {
			best = ch
			bestScore = s
		}
	}
	return best
}

func channelSpecificity(ch string) int {
	// Prereleases rank below final patches.
	if strings.Contains(ch, "-") && ch[0] >= '0' && ch[0] <= '9' {
		return 1 + strings.Count(ch, ".")
	}
	parts := strings.Split(ch, ".")
	if len(parts) >= 3 {
		return 20 + len(parts)
	}
	if len(parts) == 2 {
		return 10
	}
	return 0
}

func isArchToken(s string) bool {
	switch s {
	case "aarch64", "x86_64", "i686", "arm", "armv7", "riscv64", "powerpc64", "powerpc64le", "s390x", "wasm32":
		return true
	default:
		return false
	}
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sort.Strings(in)
	out := make([]string, 0, len(in))
	var prev string
	for i, s := range in {
		if i == 0 || s != prev {
			out = append(out, s)
			prev = s
		}
	}
	return out
}
