//go:build darwin || linux

package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
)

const (
	buildActivityTimeout   = 2 * time.Second
	buildActivityMaxOutput = 512 << 10
)

func buildActivityLsofPath() string {
	if runtime.GOOS == "linux" {
		return "/usr/bin/lsof"
	}
	return "/usr/sbin/lsof"
}

// BuildActivityProbe observes currently visible build processes immediately
// before a deep walk. It is a positive-only backoff hint: a returned map entry
// means a matching process had a cwd or open path lexically under that root.
// Missing entries, including after any failure, mean unknown rather than idle.
// It is not an authorization or a liveness/consumer proof.
type BuildActivityProbe struct {
	Runner capacity.CommandRunner
}

// Probe returns only roots with a visible go, cargo, or rustc process. It does
// not retain PIDs, commands, or observed paths. lsof itself is bounded by a
// fixed trusted path, a two-second deadline, and a 512 KiB output limit.
func (p BuildActivityProbe) Probe(ctx context.Context, roots []string) (map[string]bool, error) {
	if len(roots) == 0 {
		return map[string]bool{}, nil
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("activity roots must be absolute")
		}
	}
	runner := p.Runner
	if runner == nil {
		runner = capacity.ExecRunner{MaxOutput: buildActivityMaxOutput}
	}
	probeCtx, cancel := context.WithTimeout(ctx, buildActivityTimeout)
	defer cancel()
	// -c narrows lsof at the source. lsof's command matching is prefix-based,
	// so the parser still requires exact c fields from external output.
	raw, err := runner.Run(probeCtx, buildActivityLsofPath(),
		"-nP", "-F0pcfn", "-c", "go", "-c", "cargo", "-c", "rustc")
	if err != nil {
		return nil, fmt.Errorf("build activity probe unavailable: %w", err)
	}
	if err := probeCtx.Err(); err != nil {
		return nil, fmt.Errorf("build activity probe unavailable: %w", err)
	}
	if len(raw) > buildActivityMaxOutput {
		return nil, fmt.Errorf("build activity probe unavailable: output exceeded limit")
	}
	return parseBuildActivityLsof(raw, roots), nil
}

// parseBuildActivityLsof consumes lsof's NUL-delimited field protocol. A path
// is considered only after an exact recognized command field in the same
// process record. This keeps misleading path-like command names and adjacent
// process records from becoming activity evidence.
func parseBuildActivityLsof(raw []byte, roots []string) map[string]bool {
	active := make(map[string]bool)
	seenProcess := false
	seenFile := false
	buildProcess := false
	for _, field := range strings.Split(string(raw), "\x00") {
		if len(field) == 0 {
			continue
		}
		// lsof's -F0 protocol still uses a newline before process and file
		// records. It is a record separator only at the beginning of a field;
		// stripping it from an n field would alter the observed pathname.
		if field[0] == '\n' && len(field) > 1 && (field[1] == 'p' || field[1] == 'f' || field[1] == 'c') {
			field = field[1:]
		}
		switch field[0] {
		case 'p':
			seenProcess = true
			seenFile = false
			buildProcess = false
		case 'c':
			if seenProcess {
				buildProcess = isBuildCommand(field[1:])
			}
		case 'f':
			if seenProcess {
				seenFile = true
			}
		case 'n':
			if !seenProcess || !seenFile || !buildProcess {
				continue
			}
			for _, root := range roots {
				if pathInRoot(root, field[1:]) {
					active[root] = true
				}
			}
		}
	}
	return active
}

func isBuildCommand(command string) bool {
	switch command {
	case "go", "cargo", "rustc":
		return true
	default:
		return false
	}
}

// pathInRoot is deliberately lexical. A visible open path is only a reason to
// defer a walk, so an unresolvable spelling stays harmless; it never becomes a
// deletion admission check.
func pathInRoot(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
