package observe

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Engine runs one observe cycle (sample → hysteresis → growth → proposals).
// Not a long-running daemon; safe for CLI one-shot and future spanwitd loops.
type Engine struct {
	Clock   Clock
	Sampler Sampler
	Store   Store
	// Lease, when set, is acquired for every public state RMW (cross-process exclusivity).
	Lease CycleLease

	// MaxConcurrent is max simultaneous samples (default 1).
	MaxConcurrent int

	// opMu serializes all public RMW on this Engine (in-process exclusivity).
	// Nested work uses private *Unlocked helpers on the same call stack — never a
	// shared leaseHeld boolean that peer goroutines could misread as re-entry.
	opMu sync.Mutex
}

// withLease serializes this Engine's public operations, then acquires the
// cross-process cycle lease (if configured) for the duration of fn.
func (e *Engine) withLease(ctx context.Context, fn func() error) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if e.Lease != nil {
		if err := e.Lease.Acquire(ctx); err != nil {
			return err
		}
		defer func() { _ = e.Lease.Release() }()
	}
	return fn()
}

// Register adds or updates a volume by resolving path → opaque volume id.
// Same-ID path/alias updates are coverage transitions: they do not invent recovery
// or merge foreign identities. Identity is VolumeID only.
// Acquires the state-dir cycle lease (shared with ObserveOnce).
func (e *Engine) Register(ctx context.Context, path string, label string) (RegisteredVolume, error) {
	var out RegisteredVolume
	err := e.withLease(ctx, func() error {
		var err error
		out, err = e.registerUnlocked(ctx, path, label)
		return err
	})
	return out, err
}

// registerUnlocked performs registration without acquiring the lease (caller holds it).
func (e *Engine) registerUnlocked(ctx context.Context, path string, label string) (RegisteredVolume, error) {
	path = filepath.Clean(path)
	if len(path) > MaxRegisterPathLen {
		return RegisteredVolume{}, fmt.Errorf("register path exceeds max length %d", MaxRegisterPathLen)
	}
	label = BoundString(label, MaxLabelLen)
	s, err := e.sampler().Sample(ctx, path)
	if err != nil {
		return RegisteredVolume{}, err
	}
	doc, _ := e.loadBaselineWithSignal()
	id := string(s.VolumeID)
	v := doc.Volumes[id]
	if v == nil {
		if len(doc.Volumes) >= MaxRegisteredVolumes {
			return RegisteredVolume{}, fmt.Errorf("registration refused: max %d volumes", MaxRegisteredVolumes)
		}
		v = &VolumeRuntime{
			Volume:   RegisteredVolume{ID: s.VolumeID, RegisterPath: path, Label: label},
			Level:    LevelOK,
			Coverage: CoverageIncomparable,
			NextSeq:  1,
		}
		doc.Volumes[id] = v
	} else {
		// Same opaque id, new path: alias change is a coverage transition, not recovery.
		if v.Volume.RegisterPath != "" && v.Volume.RegisterPath != path {
			if v.Coverage == CoverageComplete {
				v.Coverage = CoveragePartial
			}
			v.ReconcileNeeded = true
			v.LossReason = "alias_path_change"
		}
		v.Volume.RegisterPath = path
		if label != "" {
			v.Volume.Label = label
		}
	}
	if err := e.store().Save(doc); err != nil {
		return RegisteredVolume{}, err
	}
	return v.Volume, nil
}

// SetForeground marks a volume's scans as lower priority in *persisted* state.
// Prefer ObserveOpts.Foreground for cycle-scoped CLI throttling (does not persist).
func (e *Engine) SetForeground(id VolumeID, on bool) error {
	return e.withLease(context.Background(), func() error {
		doc := e.loadBaseline()
		v := doc.Volumes[string(id)]
		if v == nil {
			return fmt.Errorf("volume %s not registered", id)
		}
		v.Foreground = on
		return e.store().Save(doc)
	})
}

// RecordLoss marks scope degraded and schedules exactly one reconcile.
// LastSuccess is retained for display only; growth is suppressed until a
// successful post-reconcile sample establishes a new complete baseline.
func (e *Engine) RecordLoss(id VolumeID, reason string) error {
	if !validLossReason(reason) || reason == "" {
		return fmt.Errorf("invalid loss reason %q", reason)
	}
	return e.withLease(context.Background(), func() error {
		doc, _ := e.loadBaselineWithSignal()
		v := doc.Volumes[string(id)]
		if v == nil {
			return fmt.Errorf("volume %s not registered", id)
		}
		v.Coverage = CoverageDegraded
		v.LossReason = reason
		v.ReconcileNeeded = true
		return e.store().Save(doc)
	})
}

// CycleResult is the outcome of one ObserveOnce call.
type CycleResult struct {
	Health    Health
	Notifies  []string // volume ids whose level newly crossed for notification
	Mutations []string // always empty for audit — observe path does not mutate patient data
	// SampleConcurrency is the effective max concurrent samples used this cycle
	// (clamped to 1 when foreground is active for the cycle).
	SampleConcurrency int
}

// ObserveOnce samples each registered volume (or paths), updates state, returns health+proposals.
func (e *Engine) ObserveOnce(ctx context.Context, opts ObserveOpts) (out CycleResult, err error) {
	if err := ctx.Err(); err != nil {
		return CycleResult{}, err
	}

	// Exclusive state-dir cycle lease (cross-process): hold through durable Save.
	// Nested registration during this cycle uses registerUnlocked (same stack);
	// peer goroutines serialize on opMu then compete for the cross-process lease.
	err = e.withLease(ctx, func() error {
		var innerErr error
		out, innerErr = e.observeOnceUnlocked(ctx, opts)
		return innerErr
	})
	return out, err
}

func (e *Engine) observeOnceUnlocked(ctx context.Context, opts ObserveOpts) (CycleResult, error) {
	doc, stateReset := e.loadBaselineWithSignal()

	// Optional ad-hoc register paths for this cycle (serial; lease already held).
	for _, p := range opts.RegisterPaths {
		if _, err := e.registerUnlocked(ctx, p, ""); err != nil {
			return CycleResult{}, err
		}
	}
	doc2, reset2 := e.loadBaselineWithSignal()
	doc = doc2
	if reset2 {
		stateReset = true
	}

	// Cycle-scoped foreground: throttle this invocation only; do not persist sticky FG.
	cycleFG := opts.Foreground

	ids := make([]string, 0, len(doc.Volumes))
	for id := range doc.Volumes {
		ids = append(ids, id)
	}
	// When cycle FG is off but persisted FG exists (daemon SetForeground), still honor.
	sort.SliceStable(ids, func(i, j int) bool {
		fi := cycleFG || (doc.Volumes[ids[i]] != nil && doc.Volumes[ids[i]].Foreground)
		fj := cycleFG || (doc.Volumes[ids[j]] != nil && doc.Volumes[ids[j]].Foreground)
		if fi != fj {
			return !fi && fj // non-foreground first
		}
		return ids[i] < ids[j]
	})

	notifies := make([]string, 0)
	growth := map[VolumeID]GrowthResult{}
	// Hard-bound workers: min(configured, hardMax, useful volume count).
	maxC := EffectiveSampleConcurrency(e.MaxConcurrent, len(ids))
	anyFG := cycleFG
	if !anyFG {
		for _, id := range ids {
			if v := doc.Volumes[id]; v != nil && v.Foreground {
				anyFG = true
				break
			}
		}
	}
	if anyFG && maxC > 1 {
		maxC = 1
	}

	var mu sync.Mutex
	var sampleErr error
	var sampleErrCode string
	completed := make(map[string]bool, len(ids))

	// Fixed worker pool of size maxC; unbuffered jobs channel (no O(volumes) buffer).
	jobs := make(chan string)
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for id := range jobs {
			if ctx.Err() != nil {
				continue
			}
			mu.Lock()
			v := doc.Volumes[id]
			if v == nil || v.ScanInFlight {
				mu.Unlock()
				continue
			}
			v.ScanInFlight = true
			path := v.Volume.RegisterPath
			needReconcile := v.ReconcileNeeded
			prev := v.LastSuccess
			mu.Unlock()

			s, err := e.sampler().Sample(ctx, path)
			now := e.clock().Now()

			mu.Lock()
			v = doc.Volumes[id]
			if v == nil {
				mu.Unlock()
				continue
			}
			v.ScanInFlight = false
			if err != nil {
				// Coverage gap: partial + reconcile boundary (no growth across gap).
				fail := Sample{
					CapturedAt: now,
					VolumeID:   v.Volume.ID,
					Basis:      BasisStatfs,
					Coverage:   CoveragePartial,
					Level:      v.Level,
					Source:     "sample_error",
				}
				v.LastAttempt = &fail
				v.Coverage = CoveragePartial
				v.ReconcileNeeded = true
				if v.LossReason == "" {
					v.LossReason = "sample_error"
				}
				if sampleErr == nil {
					sampleErr = err
					sampleErrCode = "sample_error/statfs_failed"
				}
				completed[id] = true
				mu.Unlock()
				continue
			}
			if s.VolumeID != v.Volume.ID {
				v.Coverage = CoverageDegraded
				v.LossReason = LossIdentityShift
				v.ReconcileNeeded = true
				v.LastAttempt = &Sample{
					CapturedAt: now,
					VolumeID:   s.VolumeID,
					// MountPath intentionally omitted from LastAttempt promotion to health.
					Basis:    s.Basis,
					Coverage: CoverageDegraded,
					Level:    s.Level,
					Source:   "identity_mismatch",
				}
				completed[id] = true
				mu.Unlock()
				continue
			}

			s.CapturedAt = now
			s.Seq = v.NextSeq
			v.NextSeq++
			s.Level = NextLevel(v.Level, s.AvailBytes, s.UsedPercent)
			if s.Coverage == "" {
				s.Coverage = CoverageComplete
			}
			// Never store raw mount paths in success samples for health safety.
			s.MountPath = ""

			if s.Coverage != CoverageComplete {
				// Non-complete sample is a coverage gap / reconcile boundary.
				v.Coverage = s.Coverage
				v.ReconcileNeeded = true
				if v.LossReason == "" {
					v.LossReason = "incomplete_sample"
				}
				v.LastAttempt = &s
				completed[id] = true
				mu.Unlock()
				continue
			}

			if needReconcile || v.ReconcileNeeded {
				// First complete after gap: new baseline only — no growth.
				v.ReconcileNeeded = false
				v.LossReason = ""
				v.Coverage = CoverageComplete
				if ShouldNotify(v.LastNotifiedLevel, s.Level) {
					notifies = append(notifies, string(v.Volume.ID))
					v.LastNotifiedLevel = s.Level
				}
				v.Level = s.Level
				v.LastAttempt = &s
				v.LastSuccess = &s
				completed[id] = true
				mu.Unlock()
				continue
			}

			v.Coverage = CoverageComplete
			if ShouldNotify(v.LastNotifiedLevel, s.Level) {
				notifies = append(notifies, string(v.Volume.ID))
				v.LastNotifiedLevel = s.Level
			}
			v.Level = s.Level
			v.LastAttempt = &s
			if prev != nil {
				g := ComputeGrowth(*prev, s)
				growth[v.Volume.ID] = g
			}
			v.LastSuccess = &s
			completed[id] = true
			mu.Unlock()
		}
	}

	for i := 0; i < maxC; i++ {
		wg.Add(1)
		go worker()
	}
	// Producer: push ids until cancelled; never buffer O(volumes).
prod:
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- id:
		case <-ctx.Done():
			break prod
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		mu.Lock()
		for _, id := range ids {
			if completed[id] {
				continue
			}
			if v := doc.Volumes[id]; v != nil {
				v.Coverage = CoveragePartial
				v.ReconcileNeeded = true
				v.LossReason = LossDroppedWork
			}
		}
		mu.Unlock()
	}

	if err := e.store().Save(doc); err != nil {
		return CycleResult{}, err
	}

	// Build health (path-safe: volume ids and labels only in primary fields).
	vols := make([]*VolumeRuntime, 0, len(doc.Volumes))
	vh := make([]VolumeHealth, 0, len(doc.Volumes))
	backlog := 0
	for _, id := range ids {
		v := doc.Volumes[id]
		if v == nil {
			continue
		}
		vols = append(vols, v)
		if v.ReconcileNeeded {
			backlog++
		}
		// Report cycle-scoped FG for display; do not require sticky state.
		rowFG := cycleFG || v.Foreground
		row := VolumeHealth{
			VolumeID:          v.Volume.ID,
			Label:             BoundString(v.Volume.Label, MaxLabelLen),
			Level:             v.Level,
			Coverage:          v.Coverage,
			LossReason:        BoundString(v.LossReason, MaxStateStringLen),
			ReconcileNeeded:   v.ReconcileNeeded,
			LastNotifiedLevel: v.LastNotifiedLevel,
			Foreground:        rowFG,
		}
		if v.LastSuccess != nil {
			row.LastSuccessAt = v.LastSuccess.CapturedAt.UTC().Format(timeRFC3339)
		}
		if v.LastAttempt != nil {
			row.LastAttemptAt = v.LastAttempt.CapturedAt.UTC().Format(timeRFC3339)
		}
		if g, ok := growth[v.Volume.ID]; ok {
			gg := g
			row.Growth = &gg
		}
		vh = append(vh, row)
	}

	proposals := BuildProposals(vols, growth)
	contract := opts.MutationContract
	if contract == "" {
		contract = "open"
	}
	h := Health{
		Schema:            HealthSchemaID,
		Version:           1,
		GeneratedAt:       e.clock().Now(),
		Volumes:           vh,
		Proposals:         proposals,
		MutationContract:  contract,
		ForegroundActive:  anyFG,
		BacklogReconciles: backlog,
		Known: CapStrings([]string{
			"observe samples pressure via capacity collector (statfs-class)",
			"proposals are notes only unless catalog evidence is supplied",
			"local observe state is bounded telemetry written by this command",
		}, MaxHealthKnown),
		Unavailable: []string{},
		Unexplained: []string{},
	}
	if stateReset {
		h.Unavailable = append(h.Unavailable, "corrupt_or_incompatible_state: reset to empty incomparable baseline (no stale growth/runway)")
	}
	for _, g := range growth {
		if g.Status == CoverageUnavailable || g.Status == CoverageIndeterminate {
			h.Unavailable = append(h.Unavailable, fmt.Sprintf("growth volume=%s status=%s detail=%s", g.SameVolumeID, g.Status, g.Detail))
		}
	}
	if sampleErr != nil {
		// Path-safe: never embed raw err.Error() (may contain register paths).
		code := sampleErrCode
		if code == "" {
			code = "sample_error"
		}
		h.Unavailable = append(h.Unavailable, code)
		_ = sampleErr // retained only for local diagnostics if needed later
	}
	h.Unavailable = CapStrings(h.Unavailable, MaxHealthUnavailable)

	return CycleResult{
		Health:            h,
		Notifies:          notifies,
		Mutations:         nil,
		SampleConcurrency: maxC,
	}, nil
}

// ObserveOpts configures one cycle.
type ObserveOpts struct {
	RegisterPaths []string
	// Foreground throttles this cycle only (not persisted).
	Foreground bool
	// MutationContract is the invocation mutation contract: open | read_only.
	MutationContract string
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func (e *Engine) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return RealClock{}
}

func (e *Engine) sampler() Sampler {
	if e.Sampler != nil {
		return e.Sampler
	}
	return CapacitySampler{}
}

func (e *Engine) store() Store {
	if e.Store != nil {
		return e.Store
	}
	return &MemoryStore{}
}

// loadBaselineWithSignal returns an in-memory empty baseline when load fails.
// Invalid state is never overwritten merely by loading it.
func (e *Engine) loadBaselineWithSignal() (*StateDocument, bool) {
	doc, err := e.store().Load()
	if err != nil {
		return emptyState(), true
	}
	return doc, false
}

// loadBaseline returns state or empty document when load fails (incomparable).
func (e *Engine) loadBaseline() *StateDocument {
	doc, _ := e.loadBaselineWithSignal()
	return doc
}

// HealthText renders a path-safe human summary (no raw user home trees).
func HealthText(h Health) string {
	var b strings.Builder
	b.WriteString("spanwit observe (propose-only; mutation_contract=" + h.MutationContract + ")\n")
	if len(h.Unavailable) > 0 {
		b.WriteString("Unavailable:\n")
		for _, reason := range h.Unavailable {
			b.WriteString("  " + reason + "\n")
		}
	}
	if len(h.Volumes) == 0 {
		b.WriteString("No registered volumes. Use: spanwit observe register <path>\n")
		return b.String()
	}
	for _, v := range h.Volumes {
		_, _ = fmt.Fprintf(&b, "- volume %s level=%s coverage=%s", v.VolumeID, v.Level, v.Coverage)
		if v.ReconcileNeeded {
			b.WriteString(" reconcile_needed")
		}
		if v.Growth != nil {
			b.WriteString(" growth=" + v.Growth.Status)
		}
		b.WriteString("\n")
	}
	if len(h.Proposals) > 0 {
		b.WriteString("Proposals (not executed):\n")
		for _, p := range h.Proposals {
			_, _ = fmt.Fprintf(&b, "  [%s] %s", p.Kind, p.Title)
			if p.RecipeID != "" {
				b.WriteString(" recipe=" + p.RecipeID)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// IsCycleBusy reports whether err is a cycle-lease contention failure.
func IsCycleBusy(err error) bool {
	return errors.Is(err, ErrCycleBusy)
}
