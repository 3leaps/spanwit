package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/3leaps/spanwit/internal/config"
)

// Candidate execution states. A match is discovered independently of whether it
// is eligible for deletion: discovery and execution-eligibility are separate
// states.
const (
	// CandidateStatePrunable marks a match that --execute may delete.
	CandidateStatePrunable = "prunable"
	// CandidateStateWithheld marks a match that is reported but excluded from the
	// --execute delete set.
	CandidateStateWithheld = "withheld"

	// WithheldReasonSafeToPrune is recorded when a signature-backed match is
	// withheld because its signature is not marked safe_to_prune (fail-closed:
	// absent/false => not safe).
	WithheldReasonSafeToPrune = "safe_to_prune"
	// WithheldReasonAge is recorded when a match fails the age window
	// (min_age older-than floor and/or max_age younger-than ceiling).
	WithheldReasonAge = "age"
	// WithheldReasonMinSize is recorded when a complete measurement is below the
	// active min_size floor.
	WithheldReasonMinSize = "min_size"
	// WithheldReasonIndeterminate is recorded when a depth-bounded lower bound
	// is below the active min_size floor, so the threshold cannot be decided
	// (true size may still meet the floor). Not matched and not excluded as small.
	WithheldReasonIndeterminate = "indeterminate"
	// WithheldReasonDomainDisabled is recorded when a signature-backed match is
	// withheld because its use domain is not in an explicitly configured
	// domains.enabled set (authorize-narrow). This is a fail-closed restriction
	// on the existing execute path, never a new execute path.
	WithheldReasonDomainDisabled = "domain_disabled"

	// FilterDisplayNone is the operator-visible value when a gate is unset.
	FilterDisplayNone = "none"
	// FilterDisplayMixed is used on the plan-level banner when candidates
	// resolved to more than one distinct value for that gate.
	FilterDisplayMixed = "mixed"
)

// EffectiveFilters records resolved size/age policy. On the plan, this is a
// truthful aggregate over discovered candidates (or defaults when none). On a
// candidate, it is the exact defaults→path→target resolution used for that row.
// Age window: min_age = older-than floor, max_age = younger-than ceiling.
type EffectiveFilters struct {
	MinSize string // e.g. "100M", "none", or plan-level "mixed"
	MinAge  string // e.g. "90d", "none", or plan-level "mixed"
	MaxAge  string // e.g. "7d", "none", or plan-level "mixed"
}

type PrunePlan struct {
	Candidates []PruneCandidate
	TotalSize  int64
	// SizeIncomplete is true when any candidate was sized under a depth bound
	// that stopped descent (totals are then lower bounds, not exact reclaim).
	SizeIncomplete   bool
	Warnings         []error
	EffectiveFilters EffectiveFilters
	// SortMode is the ranking policy applied (size|idle).
	SortMode string
	// ReclaimScope is the plan-wide reclaim granularity (whole|incremental).
	ReclaimScope string
}

type PruneCandidate struct {
	Path string
	Size int64
	// SizeIncomplete is true when Size was measured with a depth bound that
	// prevented a full-tree walk under Path.
	SizeIncomplete bool
	// State is CandidateStatePrunable or CandidateStateWithheld.
	State string
	// WithheldReason is set only when State is CandidateStateWithheld.
	WithheldReason string
	Pattern        string
	Signature      string
	Confidence     string
	Evidence       []string
	// Effective* are the resolved gate strings for this match
	// (defaults → path → target), never plan-level "mixed".
	EffectiveMinSize string
	EffectiveMinAge  string
	EffectiveMaxAge  string
	// LastActivityAt is newest observed descendant file mtime (zero if unknown).
	LastActivityAt time.Time
	// ActivityBasis is ActivityBasisDescendantMtime or ActivityBasisUnknown.
	ActivityBasis string
	// ActivityIncomplete is true when activity may be understated.
	ActivityIncomplete bool
	// RebuildExpectation is advisory refill likelihood (high|medium|low|"").
	RebuildExpectation string
	// ReclaimScope is whole (default) or incremental.
	ReclaimScope string
	// ParentPath is the verified cargo-target parent when ReclaimScope is partial.
	ParentPath string
	// AuthorizationBoundary is the non-inferred root that authorized this
	// candidate (config profile root or carried analysis_root). Execute refuses a
	// candidate not contained under it. Never derived from the candidate path,
	// its parent, catalog metadata, or a bare allowlist.
	AuthorizationBoundary string
	// ContainmentRelation is the permitted boundary relation (descendant default).
	ContainmentRelation string
	// ProvenanceKind names the authorization source; empty/unknown fails closed
	// at execute (see engine.Provenance* constants).
	ProvenanceKind string
	Deleted        bool
}

// PlanOptions tunes plan construction. Zero value preserves historical prune
// behavior (unlimited candidate sizing depth, size sort, whole-target reclaim).
type PlanOptions struct {
	// SizeMaxDepth bounds DirSize under each candidate path. <0 or unset via
	// BuildPrunePlan defaults to unlimited (-1). space uses a non-negative
	// budget so triage cannot unbounded-du large verified targets.
	SizeMaxDepth int
	// sizeMaxDepthSet distinguishes explicit 0 from unset when using options.
	sizeMaxDepthSet bool
	// SortMode is SortSize (default) or SortIdle.
	SortMode string
	// ReclaimScope is ReclaimScopeWhole (default) or ReclaimScopeIncremental.
	ReclaimScope string
}

// WithSizeMaxDepth returns PlanOptions that bound candidate sizing depth.
func WithSizeMaxDepth(depth int) PlanOptions {
	return PlanOptions{SizeMaxDepth: depth, sizeMaxDepthSet: true}
}

// WithSortMode sets candidate ordering (SortSize or SortIdle).
func WithSortMode(mode string) PlanOptions {
	return PlanOptions{SortMode: mode}
}

// WithReclaimScope selects whole-target vs incremental-only candidates.
func WithReclaimScope(scope string) PlanOptions {
	return PlanOptions{ReclaimScope: scope}
}

// MergePlanOptions folds option values left-to-right (later wins when set).
func MergePlanOptions(opts ...PlanOptions) PlanOptions {
	var out PlanOptions
	for _, o := range opts {
		if o.sizeMaxDepthSet {
			out.SizeMaxDepth = o.SizeMaxDepth
			out.sizeMaxDepthSet = true
		}
		if o.SortMode != "" {
			out.SortMode = o.SortMode
		}
		if o.ReclaimScope != "" {
			out.ReclaimScope = o.ReclaimScope
		}
	}
	return out
}

type CandidateJob struct {
	Path                  string
	State                 string
	WithheldReason        string
	Pattern               string
	Signature             string
	Confidence            string
	Evidence              []string
	MinSize               int64
	EffectiveMinSize      string
	EffectiveMinAge       string
	EffectiveMaxAge       string
	ReclaimScope          string
	ParentPath            string
	AuthorizationBoundary string
	ContainmentRelation   string
	ProvenanceKind        string
}

func BuildPrunePlan(ctx context.Context, cfg *config.Config, opts ...PlanOptions) (PrunePlan, error) {
	var plan PrunePlan
	if cfg == nil {
		return plan, fmt.Errorf("config is required")
	}
	merged := MergePlanOptions(opts...)
	sizeMaxDepth := -1 // unlimited: full accuracy for scan/prune reclaim totals
	if merged.sizeMaxDepthSet {
		sizeMaxDepth = merged.SizeMaxDepth
	}
	sortMode := merged.SortMode
	if sortMode == "" {
		sortMode = SortSize
	}
	reclaimScope := merged.ReclaimScope
	if reclaimScope == "" {
		reclaimScope = ReclaimScopeWhole
	}
	if reclaimScope != ReclaimScopeWhole && reclaimScope != ReclaimScopeIncremental {
		return plan, fmt.Errorf("invalid reclaim scope %q (want whole|incremental)", reclaimScope)
	}
	signatures := config.MergeSignatureCatalogs(config.BuiltInSignatures(), cfg.Signatures)
	var jobs []CandidateJob

	for _, profile := range cfg.Paths {
		if !profile.IsEnabled() {
			continue
		}
		root, err := CleanConfiguredPath(profile.Path)
		if err != nil {
			return plan, err
		}
		if _, err := os.Stat(root); err != nil {
			return plan, fmt.Errorf("cannot access configured path %q: %w", profile.Path, err)
		}

		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				plan.Warnings = append(plan.Warnings, walkErr)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			// filepath.WalkDir does not follow symlinks: a symlink (even one
			// pointing at a directory) is reported with fs.ModeSymlink set and is
			// NOT descended into. The IsDir() guard above therefore already skips
			// symlinked "directories" (d.IsDir() is false for a symlink), so this
			// explicit ModeSymlink SkipDir is defense-in-depth: it keeps discovery
			// symlink-strict even if the entry type is reported differently, and
			// documents the guarantee so it cannot silently vanish if the walker
			// changes. A symlinked target/ is never admitted as a candidate.
			if d.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			if path == root {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				plan.Warnings = append(plan.Warnings, err)
				return filepath.SkipDir
			}
			rel = filepath.ToSlash(rel)

			if profile.MaxDepth >= 0 && pathDepth(rel) > profile.MaxDepth {
				return filepath.SkipDir
			}
			if matchesAny(rel, profile.Ignores) {
				return filepath.SkipDir
			}

			target, signature, evidence, ok := matchingTarget(root, path, rel, profile.Targets, signatures)
			if !ok {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				plan.Warnings = append(plan.Warnings, err)
				return filepath.SkipDir
			}

			effMinSize, effMinAge, effMaxAge := resolveFilterStrings(target, profile, cfg.Defaults)
			minSize, err := ParseSize(displayFilterRaw(effMinSize))
			if err != nil {
				return fmt.Errorf("invalid min_size %q: %w", effMinSize, err)
			}
			confidence := "pattern"
			pattern := target.Pattern
			if target.Signature != "" {
				confidence = signature.Confidence
				pattern = strings.Join(signature.CandidatePatterns, ",")
			}

			// Incremental mode is Cargo incremental only: expand verified
			// cargo-target parents into partial leaves; never fall through to
			// whole-directory admission for other signatures/patterns.
			if reclaimScope == ReclaimScopeIncremental {
				if target.Signature != "development.rust.cargo-target" {
					label := target.Signature
					if label == "" {
						label = "pattern:" + target.Pattern
					}
					plan.Warnings = append(plan.Warnings, fmt.Errorf(
						"reclaim_scope=incremental: skipped non-cargo candidate %s (%s); only development.rust.cargo-target/**/incremental is admitted",
						path, label,
					))
					return filepath.SkipDir
				}
				// Signature safety (not age) from parent identity; age applies to
				// each partial leaf only so exact-plan replay matches discovery.
				baseState := CandidateStatePrunable
				baseReason := ""
				if !signature.SafeToPrune {
					baseState = CandidateStateWithheld
					baseReason = WithheldReasonSafeToPrune
				}
				incs, findErr := FindCargoIncrementalDirs(path)
				if findErr != nil {
					plan.Warnings = append(plan.Warnings, fmt.Errorf("incremental under %s: %w", path, findErr))
					return filepath.SkipDir
				}
				for _, inc := range incs {
					incInfo, err := os.Lstat(inc)
					if err != nil {
						plan.Warnings = append(plan.Warnings, err)
						continue
					}
					incState := baseState
					incReason := baseReason
					if baseState != CandidateStateWithheld && !meetsAgeWindow(effMinAge, effMaxAge, incInfo.ModTime(), time.Now()) {
						incState = CandidateStateWithheld
						incReason = WithheldReasonAge
					}
					ev := append([]string{}, evidence...)
					ev = append(ev, "reclaim_scope:incremental", "parent:"+path)
					jobs = append(jobs, CandidateJob{
						Path:             inc,
						State:            incState,
						WithheldReason:   incReason,
						Pattern:          pattern,
						Signature:        target.Signature,
						Confidence:       confidence,
						Evidence:         ev,
						MinSize:          minSize,
						EffectiveMinSize: effMinSize,
						EffectiveMinAge:  effMinAge,
						EffectiveMaxAge:  effMaxAge,
						ReclaimScope:     ReclaimScopeIncremental,
						ParentPath:       path,
						// Authorization boundary is the enabled profile root (never
						// inferred from the candidate). Incremental leaves are deep
						// strict descendants of it.
						AuthorizationBoundary: root,
						ContainmentRelation:   ContainmentDescendant,
						ProvenanceKind:        ProvenanceConfigProfile,
					})
				}
				_ = info // parent mtime is not an age gate for partial leaves
				return filepath.SkipDir
			}

			// Whole-target mode: age window on the matched path itself.
			state := CandidateStatePrunable
			withheldReason := ""
			if !meetsAgeWindow(effMinAge, effMaxAge, info.ModTime(), time.Now()) {
				state = CandidateStateWithheld
				withheldReason = WithheldReasonAge
			}
			if target.Signature != "" && !signature.SafeToPrune && state != CandidateStateWithheld {
				state = CandidateStateWithheld
				withheldReason = WithheldReasonSafeToPrune
			}

			jobs = append(jobs, CandidateJob{
				Path:             path,
				State:            state,
				WithheldReason:   withheldReason,
				Pattern:          pattern,
				Signature:        target.Signature,
				Confidence:       confidence,
				Evidence:         evidence,
				MinSize:          minSize,
				EffectiveMinSize: effMinSize,
				EffectiveMinAge:  effMinAge,
				EffectiveMaxAge:  effMaxAge,
				ReclaimScope:     ReclaimScopeWhole,
				// Authorization boundary is the enabled profile root; the walk skips
				// path==root, so every candidate is a strict descendant.
				AuthorizationBoundary: root,
				ContainmentRelation:   ContainmentDescendant,
				ProvenanceKind:        ProvenanceConfigProfile,
			})
			return filepath.SkipDir
		})
		if err != nil {
			return plan, err
		}
	}

	// Overlapping path profiles (e.g. ~/dev and ~/dev/3leaps) can discover the
	// same absolute path more than once. De-dupe before sizing/aggregation so
	// text and JSON totals never double-count reclaimable bytes. First match
	// wins: config path order, then walk order within a profile (v0.x).
	jobs, dropped := DedupeCandidateJobs(jobs)
	if dropped > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Errorf(
			"overlapping path profiles: dropped %d duplicate candidate path(s); totals use first-match only",
			dropped,
		))
	}
	jobs, collapsed := CollapseContainment(jobs)
	if collapsed > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Errorf(
			"containment: dropped %d nested candidate path(s); plan keeps ancestors only",
			collapsed,
		))
	}

	plan.EffectiveFilters = aggregateEffectiveFilters(jobs, cfg.Defaults)
	candidates, warnings := sizeCandidateJobs(ctx, jobs, sizeMaxDepth)
	plan.Candidates = append(plan.Candidates, candidates...)
	plan.Warnings = append(plan.Warnings, warnings...)
	// Authorize-narrow: when domains.enabled is explicitly configured, withhold
	// prunable signature candidates whose use domain is not enabled. Applied only
	// when configured so an omitted block preserves current feel (custom
	// signatures in any domain remain prunable). Fail-closed; no new execute path.
	if enabled := config.ResolveEnabledDomains(cfg); enabled.Configured {
		applyDomainGate(plan.Candidates, enabled)
	}
	for _, candidate := range plan.Candidates {
		plan.TotalSize += candidate.Size
		if candidate.SizeIncomplete {
			plan.SizeIncomplete = true
		}
	}
	SortCandidates(plan.Candidates, sortMode)
	plan.SortMode = sortMode
	plan.ReclaimScope = reclaimScope
	return plan, nil
}

// applyDomainGate flips prunable signature-backed candidates to withheld when
// their use domain is not in the enabled set. Pattern-only candidates (no
// signature spine) carry no domain and are never gated. Returns the count gated.
func applyDomainGate(candidates []PruneCandidate, enabled config.EnabledDomains) int {
	gated := 0
	for i := range candidates {
		c := &candidates[i]
		if c.State != CandidateStatePrunable || c.Signature == "" {
			continue
		}
		domain := domainSegment(c.Signature)
		if domain == "" || enabled.Enabled(domain) {
			continue
		}
		c.State = CandidateStateWithheld
		c.WithheldReason = WithheldReasonDomainDisabled
		gated++
	}
	return gated
}

// domainSegment returns the first (domain) segment of a domain.ecosystem.name id.
func domainSegment(id string) string {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		return id[:i]
	}
	return id
}

// dedupeCandidateJobs keeps the first job for each cleaned absolute path.
// Later duplicates (from nested/overlapping path profiles) are dropped.
// Returns the unique list and the number of discarded jobs.
func DedupeCandidateJobs(jobs []CandidateJob) (unique []CandidateJob, dropped int) {
	if len(jobs) == 0 {
		return nil, 0
	}
	seen := make(map[string]struct{}, len(jobs))
	unique = make([]CandidateJob, 0, len(jobs))
	for _, job := range jobs {
		key := candidatePathKey(job.Path)
		if _, ok := seen[key]; ok {
			dropped++
			continue
		}
		seen[key] = struct{}{}
		job.Path = key
		unique = append(unique, job)
	}
	return unique, dropped
}

// candidatePathKey normalizes a filesystem path for identity comparison across
// overlapping profile roots (Clean + absolute).
func candidatePathKey(path string) string {
	cleaned := filepath.Clean(path)
	if abs, err := filepath.Abs(cleaned); err == nil {
		return abs
	}
	return cleaned
}

func matchingTarget(root string, path string, rel string, targets []config.Target, signatures config.SignatureCatalog) (config.Target, config.Signature, []string, bool) {
	for _, target := range targets {
		if target.Signature != "" {
			signature, ok := signatures.Lookup(target.Signature)
			if !ok {
				continue
			}
			if !matchesAny(rel, signature.CandidatePatterns) {
				continue
			}
			evidence, ok := signatureEvidence(root, path, signature)
			if !ok {
				continue
			}
			return target, signature, evidence, true
		}
		if target.Pattern != "" && matchesPattern(rel, target.Pattern) {
			return target, config.Signature{}, []string{"pattern:" + target.Pattern}, true
		}
	}
	return config.Target{}, config.Signature{}, nil, false
}

func matchesAny(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchesPattern(rel, pattern) {
			return true
		}
	}
	return false
}

func matchesPattern(rel string, pattern string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	if pattern == "" {
		return false
	}
	if ok, _ := doublestar.PathMatch(pattern, rel); ok {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		if ok, _ := doublestar.PathMatch(base, rel); ok {
			return true
		}
	}
	return false
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// signatureEvidence gathers the ancestor/child evidence a signature requires.
// Discovery is symlink-strict, so evidence gathering must be too: configured
// evidence names are validated as local relative paths (no absolute, empty
// segment, or ".." escape), each resolved entry is inspected with Lstat (never
// following a symlink), and a symlinked evidence entry never contributes
// deletion authority. A malicious required/child that would follow a link into a
// surprising place fails closed (no match).
func signatureEvidence(root string, path string, signature config.Signature) ([]string, bool) {
	var evidence []string
	for _, required := range signature.RequiredAncestorFiles {
		if !isLocalRelativeName(required) {
			return evidence, false
		}
		if found, ok := findAncestorFile(root, filepath.Dir(path), required); ok {
			evidence = append(evidence, "ancestor:"+found)
		} else {
			return evidence, false
		}
	}
	if len(signature.AnyChildPaths) > 0 {
		for _, child := range signature.AnyChildPaths {
			if !isLocalRelativeName(child) {
				continue
			}
			candidate := filepath.Join(path, filepath.FromSlash(child))
			// isLocalRelativeName already forbids "..", so candidate cannot escape
			// path; this is a defensive re-check of the resolved relationship.
			if !isWithinDir(path, candidate) {
				continue
			}
			// Child evidence must be a plain file or directory with no symlink
			// component (leaf or intermediate); devices/FIFOs/sockets/symlinks
			// never satisfy evidence.
			if !isPlainEvidence(candidate, false) {
				continue
			}
			evidence = append(evidence, "child:"+child)
			return evidence, true
		}
		return evidence, false
	}
	return evidence, true
}

// findAncestorFile walks from start up to (and including) root looking for name.
// A match must be a regular file with no symlink component anywhere in its path
// so a planted symlink or a directory cannot forge required ancestor evidence.
// The search is bounded to root; it never ascends past the authorized root.
func findAncestorFile(root string, start string, name string) (string, bool) {
	for {
		candidate := filepath.Join(start, name)
		if isPlainEvidence(candidate, true) {
			return candidate, true
		}
		if start == root {
			return "", false
		}
		next := filepath.Dir(start)
		if next == start {
			return "", false
		}
		start = next
	}
}

// isPlainEvidence reports whether path is a real evidence entry with no symlink
// component: a regular file when regularOnly, otherwise a regular file or a
// directory. A symlink (leaf OR any intermediate component), device, FIFO, or
// socket is rejected, so evidence can neither follow a link into a surprising
// place nor be satisfied by a special file.
func isPlainEvidence(path string, regularOnly bool) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if regularOnly {
		if !info.Mode().IsRegular() {
			return false
		}
	} else if !info.IsDir() && !info.Mode().IsRegular() {
		return false
	}
	// Reject a symlink in any intermediate component of the resolved path.
	return ValidatePathNoSymlinkComponents(path) == nil
}

// isLocalRelativeName reports whether name is a safe local relative path: not
// empty, not absolute, no Windows volume, and no empty/"."/".." segment.
func isLocalRelativeName(name string) bool {
	if name == "" {
		return false
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return false
	}
	s := filepath.ToSlash(name)
	if strings.HasPrefix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// isWithinDir reports whether candidate resolves to a strict descendant of dir
// (component-aware; no string-prefix confusion).
func isWithinDir(dir string, candidate string) bool {
	rel, err := filepath.Rel(dir, candidate)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return true
}

func sizeCandidateJobs(ctx context.Context, jobs []CandidateJob, sizeMaxDepth int) ([]PruneCandidate, []error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	workers := runtime.NumCPU()
	if workers > len(jobs) {
		workers = len(jobs)
	}

	jobCh := make(chan CandidateJob)
	var mu sync.Mutex
	var candidates []PruneCandidate
	var warnings []error
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if err := ctx.Err(); err != nil {
					mu.Lock()
					warnings = append(warnings, err)
					mu.Unlock()
					return
				}
				// sizeMaxDepth < 0: full-tree reclaim accuracy (scan/prune default).
				// space passes a non-negative budget for triage-bounded sizing.
				sized, err := DirSizeContext(ctx, job.Path, sizeMaxDepth)
				mu.Lock()
				if err != nil {
					warnings = append(warnings, err)
				} else {
					state := job.State
					withheldReason := job.WithheldReason
					// Size floor is applied after measurement. Below-floor matches
					// are withheld (visible), not dropped. Keep an earlier filter
					// or safety reason if already withheld.
					// Complete below floor → min_size (excluded). Incomplete lower
					// bound below floor → indeterminate (threshold undecidable).
					if sized.Bytes < job.MinSize && state != CandidateStateWithheld {
						state = CandidateStateWithheld
						if sized.Incomplete {
							withheldReason = WithheldReasonIndeterminate
						} else {
							withheldReason = WithheldReasonMinSize
						}
					}
					basis := ActivityBasisDescendantMtime
					activityIncomplete := sized.ActivityIncomplete || sized.Incomplete
					if sized.NewestMtime.IsZero() {
						basis = ActivityBasisUnknown
						activityIncomplete = true
					}
					scope := job.ReclaimScope
					if scope == "" {
						scope = ReclaimScopeWhole
					}
					candidates = append(candidates, PruneCandidate{
						Path:                  job.Path,
						Size:                  sized.Bytes,
						SizeIncomplete:        sized.Incomplete,
						State:                 state,
						WithheldReason:        withheldReason,
						Pattern:               job.Pattern,
						Signature:             job.Signature,
						Confidence:            job.Confidence,
						Evidence:              job.Evidence,
						EffectiveMinSize:      job.EffectiveMinSize,
						EffectiveMinAge:       job.EffectiveMinAge,
						EffectiveMaxAge:       job.EffectiveMaxAge,
						LastActivityAt:        sized.NewestMtime,
						ActivityBasis:         basis,
						ActivityIncomplete:    activityIncomplete,
						RebuildExpectation:    DeriveRebuildExpectation(sized.NewestMtime, activityIncomplete, time.Now()),
						ReclaimScope:          scope,
						ParentPath:            job.ParentPath,
						AuthorizationBoundary: job.AuthorizationBoundary,
						ContainmentRelation:   job.ContainmentRelation,
						ProvenanceKind:        job.ProvenanceKind,
					})
				}
				mu.Unlock()
			}
		}()
	}

sendJobs:
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break sendJobs
		case jobCh <- job:
		}
	}
	close(jobCh)
	wg.Wait()
	return candidates, warnings
}

// resolveFilterStrings returns the display-form gates for a match after
// defaults → path → target precedence (empty config → "none").
// min_age is the older-than floor; max_age is the younger-than ceiling.
func resolveFilterStrings(target config.Target, profile config.PathProfile, defaults config.Defaults) (minSize, minAge, maxAge string) {
	minSize = defaults.MinSize
	if profile.MinSize != "" {
		minSize = profile.MinSize
	}
	if target.MinSize != "" {
		minSize = target.MinSize
	}
	if minSize == "" {
		minSize = FilterDisplayNone
	}

	minAge = defaults.MinAge
	if profile.MinAge != "" {
		minAge = profile.MinAge
	}
	if target.MinAge != "" {
		minAge = target.MinAge
	}
	if minAge == "" {
		minAge = FilterDisplayNone
	}

	maxAge = defaults.MaxAge
	if profile.MaxAge != "" {
		maxAge = profile.MaxAge
	}
	if target.MaxAge != "" {
		maxAge = target.MaxAge
	}
	if maxAge == "" {
		maxAge = FilterDisplayNone
	}
	return minSize, minAge, maxAge
}

// displayFilterRaw maps display "none" back to empty for parsers.
func displayFilterRaw(display string) string {
	if display == FilterDisplayNone || display == FilterDisplayMixed {
		return ""
	}
	return display
}

func effectiveFiltersFromDefaults(defaults config.Defaults) EffectiveFilters {
	minSize, minAge, maxAge := resolveFilterStrings(config.Target{}, config.PathProfile{}, defaults)
	return EffectiveFilters{MinSize: minSize, MinAge: minAge, MaxAge: maxAge}
}

// aggregateEffectiveFilters builds a plan-level banner: the single resolved
// value when every discovered job agrees, otherwise "mixed". With no jobs,
// falls back to global defaults (so empty plans still show policy).
func aggregateEffectiveFilters(jobs []CandidateJob, defaults config.Defaults) EffectiveFilters {
	if len(jobs) == 0 {
		return effectiveFiltersFromDefaults(defaults)
	}
	minSizes := map[string]struct{}{}
	minAges := map[string]struct{}{}
	maxAges := map[string]struct{}{}
	for _, job := range jobs {
		minSizes[job.EffectiveMinSize] = struct{}{}
		minAges[job.EffectiveMinAge] = struct{}{}
		maxAges[job.EffectiveMaxAge] = struct{}{}
	}
	return EffectiveFilters{
		MinSize: pickAggregate(minSizes),
		MinAge:  pickAggregate(minAges),
		MaxAge:  pickAggregate(maxAges),
	}
}

func pickAggregate(values map[string]struct{}) string {
	if len(values) == 1 {
		for v := range values {
			return v
		}
	}
	if len(values) == 0 {
		return FilterDisplayNone
	}
	return FilterDisplayMixed
}

// meetsAgeWindow evaluates file age (now − mtime) against min_age (floor) and
// max_age (ceiling). Both unset ⇒ no age gate. Eligible when
// min_age ≤ age ≤ max_age for each bound that is set.
func meetsAgeWindow(minAgeDisplay, maxAgeDisplay string, modTime time.Time, now time.Time) bool {
	fileAge := now.Sub(modTime)
	if minAge := displayFilterRaw(minAgeDisplay); minAge != "" {
		d, err := parseAge(minAge)
		if err != nil {
			return false
		}
		// Older-than floor: must be at least this old.
		if fileAge < d {
			return false
		}
	}
	if maxAge := displayFilterRaw(maxAgeDisplay); maxAge != "" {
		d, err := parseAge(maxAge)
		if err != nil {
			return false
		}
		// Younger-than ceiling: must be at most this old.
		if fileAge > d {
			return false
		}
	}
	return true
}

func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	unit := s[len(s)-1]
	value := s[:len(s)-1]
	var n int64
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return 0, err
	}
	switch unit {
	case 's':
		return time.Duration(n) * time.Second, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported age unit %q", unit)
	}
}
