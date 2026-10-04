package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Directory audit profile. The audit reuses the full-subject walk
// and aggregation core, then applies depth, an optional inclusive size floor and
// a final top-K over the selected accounting plane. It is a separate versioned
// profile: the shipped file-v0 and aggregation-v0/v1 streams are unchanged.
const (
	AggregationProfileV2 = "spanwit.filesystem-inventory-aggregation/v2"

	EmissionDirectoryAudit = "directory_audit"

	RecordHeaderV2    = "spanwit.inventory.header.v2"
	RecordDirectoryV2 = "spanwit.inventory.directory.v2"
	RecordSummaryV2   = "spanwit.inventory.summary.v2"

	// AuditOrdering names the rank documented in docs/inventory.md: definite
	// exact values, definite lower bounds, indeterminate lower bounds, then
	// unavailable values; descending bytes within a group, then root id and
	// relative path.
	AuditOrdering = "directory_audit_rank_v1"

	FloorMeets         = "meets"
	FloorIndeterminate = "indeterminate"
	FloorNotApplied    = "not_applied"
	floorExcluded      = "excluded" // never emitted

	FailureAggregateBudget   = "aggregate_directory_budget_exhausted"
	FailureAggregateOverflow = "aggregate_accounting_overflow"
	FailureTraversal         = "traversal_failed"
)

// AuditSubject declares that directory accounting covers every regular file in
// the declared traversal, independent of output selection.
type AuditSubject struct {
	AccountingScope string `json:"accounting_scope"`
	EntryType       string `json:"entry_type"`
	SymlinkPosture  string `json:"symlink_posture"`
}

// AuditDirectorySelection is the resolved output selection. FloorBytes is
// present iff FloorApplied; an explicit zero floor is applied.
type AuditDirectorySelection struct {
	FloorApplied bool   `json:"floor_applied"`
	FloorBytes   *int64 `json:"floor_bytes,omitempty"`
	SizeBasis    string `json:"size_basis"`
	Depth        int    `json:"depth"`
	Top          int    `json:"top"`
}

// AuditHeader is the header.v2 record.
type AuditHeader struct {
	RunID                   string                  `json:"-"`
	CapturedAt              time.Time               `json:"-"`
	Profile                 string                  `json:"profile"`
	Roots                   []Root                  `json:"roots"`
	Backend                 string                  `json:"backend"`
	Workers                 int                     `json:"workers"`
	MaxOpenDirs             int                     `json:"max_open_dirs"`
	MaxPendingDirs          int                     `json:"max_pending_dirs"`
	OneFilesystem           bool                    `json:"one_filesystem"`
	EmissionMode            string                  `json:"emission_mode"`
	MaxAggregateDirectories int                     `json:"max_aggregate_directories"`
	Subject                 AuditSubject            `json:"subject"`
	DirectorySelection      AuditDirectorySelection `json:"directory_selection"`
	Exclusions              []Exclusion             `json:"exclusions"`
	Ordering                string                  `json:"ordering"`
	PathProtection          string                  `json:"path_protection"`
	MutationContract        string                  `json:"mutation_contract"`
}

// AuditDirectory is the directory.v2 record: one selected aggregate with its
// typed accounting claims and explicit floor decision.
type AuditDirectory struct {
	RootID                   string              `json:"root_id"`
	RelativePath             string              `json:"relative_path"`
	Depth                    int                 `json:"depth"`
	FileCount                int64               `json:"file_count"`
	DescendantDirectoryCount int64               `json:"descendant_directory_count"`
	AllocatedUnmeasuredCount int64               `json:"allocated_unmeasured_count"`
	Lifecycle                string              `json:"lifecycle"`
	AffectingGapCount        int64               `json:"affecting_gap_count"`
	Accounting               DirectoryAccounting `json:"accounting"`
	FloorDecision            string              `json:"floor_decision"`
}

// SelectedClaim returns the accounting claim for the given basis.
func (d AuditDirectory) SelectedClaim(basis string) AccountingClaim {
	if basis == SizeAllocated {
		return d.Accounting.Allocated
	}
	return d.Accounting.Apparent
}

// AuditSelectionCounts are present only on a reconciled selection. Invariants:
// E = M + I + X + U, S = E - X, K = S or min(top, S); truncated iff K < S.
type AuditSelectionCounts struct {
	DepthEligible           int64 `json:"directory_depth_eligible_count"`
	Meets                   int64 `json:"directory_meets_count"`
	Indeterminate           int64 `json:"directory_indeterminate_count"`
	Excluded                int64 `json:"directory_excluded_count"`
	NotApplied              int64 `json:"directory_not_applied_count"`
	SelectedBeforeTop       int64 `json:"directory_selected_before_top_count"`
	PlannedEmission         int64 `json:"directory_planned_emission_count"`
	UnavailableSizeEligible int64 `json:"directory_unavailable_size_eligible_count"`
	SelectionTruncated      bool  `json:"directory_selection_truncated"`
}

// AuditFailure is the typed, path-free failure on a failed audit result. Budget
// fields are present only on the budget variant.
type AuditFailure struct {
	Code              string `json:"code"`
	Limit             *int64 `json:"limit,omitempty"`
	Retained          *int64 `json:"retained,omitempty"`
	AttemptedRetained *int64 `json:"attempted_retained,omitempty"`
}

// AuditRootSummary is per-root traversal evidence. It deliberately carries no
// file match/emission counters: this profile emits directories only.
type AuditRootSummary struct {
	RootID              string `json:"root_id"`
	Lifecycle           string `json:"lifecycle"`
	VisitedEntries      int64  `json:"visited_entries"`
	VisitedDirectories  int64  `json:"visited_directories"`
	GapCount            int64  `json:"gap_count"`
	BoundarySkipCount   int64  `json:"boundary_skip_count"`
	ExclusionCount      int64  `json:"exclusion_count"`
	VanishedCount       int64  `json:"vanished_count"`
	QueueLimitSkipCount int64  `json:"queue_limit_skip_count"`
	RemoteSkipCount     int64  `json:"remote_skip_count,omitempty"`
	StalledCount        int64  `json:"stalled_count,omitempty"`
	Canceled            bool   `json:"canceled"`
	Failed              bool   `json:"failed"`
}

// AuditSummary is the summary.v2 record. Lifecycle describes the audit result;
// Roots preserve traversal evidence. Selection counts are omitted (never zero)
// when the selection did not reconcile.
type AuditSummary struct {
	Lifecycle                            string `json:"lifecycle"`
	Canceled                             bool   `json:"canceled"`
	SelectionReconciled                  bool   `json:"selection_reconciled"`
	EmissionCompleted                    bool   `json:"emission_completed"`
	DirectoryEmittedCount                int64  `json:"directory_emitted_count"`
	DirectoryIndeterminateEmittedCount   int64  `json:"directory_indeterminate_emitted_count"`
	DirectoryUnavailableSizeEmittedCount int64  `json:"directory_unavailable_size_emitted_count"`
	*AuditSelectionCounts
	Failure                *AuditFailure      `json:"failure,omitempty"`
	Backend                string             `json:"backend"`
	VisitedEntries         int64              `json:"visited_entries"`
	VisitedDirectories     int64              `json:"visited_directories"`
	GapCount               int64              `json:"gap_count"`
	BoundarySkipCount      int64              `json:"boundary_skip_count"`
	VanishedCount          int64              `json:"vanished_count"`
	QueueLimitSkipCount    int64              `json:"queue_limit_skip_count"`
	ExclusionCount         int64              `json:"exclusion_count"`
	RemoteSkipCount        int64              `json:"remote_skip_count,omitempty"`
	StalledCount           int64              `json:"stalled_count,omitempty"`
	PeakDepth              int64              `json:"peak_depth"`
	PeakPendingDirectories int64              `json:"peak_pending_directories"`
	DurationNanos          int64              `json:"duration_nanos"`
	TerminalObservedAt     time.Time          `json:"terminal_observed_at"`
	Roots                  []AuditRootSummary `json:"roots"`
}

// AuditSink consumes the v2 directory-audit stream. Gaps use Sink.Gap and the
// unchanged gap.v1 record; file entries are never emitted in this profile.
type AuditSink interface {
	AuditHeader(AuditHeader) error
	AuditDirectory(AuditDirectory) error
	AuditSummary(AuditSummary) error
}

// auditHooks are deterministic test barriers at the walk/selection/emission
// boundaries. Production runs leave them nil.
type auditHooks struct {
	beforeSelect func()
	beforeRow    func(index int)
	afterCommit  func()
}

func auditHeader(nopts normalizedOptions) AuditHeader {
	selection := AuditDirectorySelection{
		FloorApplied: nopts.DirectoryFloor != nil,
		SizeBasis:    nopts.SizeBasis,
		Depth:        nopts.DirectoryDepth,
		Top:          nopts.DirectoryTop,
	}
	if nopts.DirectoryFloor != nil {
		floor := *nopts.DirectoryFloor
		selection.FloorBytes = &floor
	}
	return AuditHeader{
		RunID: nopts.RunID, CapturedAt: nopts.now,
		Profile: AggregationProfileV2, Roots: append([]Root(nil), nopts.roots...),
		Backend: nopts.Backend, Workers: nopts.Workers,
		MaxOpenDirs: nopts.MaxOpenDirs, MaxPendingDirs: nopts.MaxPendingDirs,
		OneFilesystem: nopts.OneFilesystem, EmissionMode: EmissionDirectoryAudit,
		MaxAggregateDirectories: nopts.MaxAggregateDirectories,
		Subject: AuditSubject{
			AccountingScope: "full_subject", EntryType: "file", SymlinkPosture: "not_followed",
		},
		DirectorySelection: selection,
		Exclusions:         append([]Exclusion{}, nopts.exclusions...),
		Ordering:           AuditOrdering,
		PathProtection:     PathProtectionSourceStructure,
		MutationContract:   nopts.MutationContract,
	}
}

// completeAudit reconciles selection and emits rows and the terminal summary.
// Precedence: fatal walk/budget error, then output error, then external
// cancellation. External cancellation observed before the emission commit
// point leaves the result partial and canceled; after commit it is ignored.
func (s *runState) completeAudit(ctx context.Context, sink AuditSink) (AuditSummary, error) {
	hooks := s.opts.auditHooks
	reconciled, committed, canceled := false, false, false
	var counts AuditSelectionCounts
	var emitted, indeterminateEmitted, unavailableEmitted int64

	selectable := s.walkError() == nil && s.outputError() == nil && ctx.Err() == nil
	if selectable && hooks != nil && hooks.beforeSelect != nil {
		hooks.beforeSelect()
	}
	if selectable && ctx.Err() == nil {
		rows, selected, err := s.aggregator.auditSelect(
			s.opts.DirectoryDepth, s.opts.DirectoryTop, s.opts.SizeBasis, s.opts.DirectoryFloor)
		if err != nil {
			s.failWalk(err)
		} else {
			reconciled, counts = true, selected
			for i, row := range rows {
				if hooks != nil && hooks.beforeRow != nil {
					hooks.beforeRow(i)
				}
				if ctx.Err() != nil {
					break
				}
				s.emitMu.Lock()
				err := sink.AuditDirectory(row)
				s.emitMu.Unlock()
				if err != nil {
					s.setOutputError(err)
					break
				}
				emitted++
				if row.FloorDecision == FloorIndeterminate {
					indeterminateEmitted++
				}
				if row.SelectedClaim(s.opts.SizeBasis).Status == ClaimStatusUnsupported {
					unavailableEmitted++
				}
			}
			committed = s.outputError() == nil && emitted == selected.PlannedEmission
		}
	}
	if committed && hooks != nil && hooks.afterCommit != nil {
		hooks.afterCommit()
	}
	if !committed && s.walkError() == nil && s.outputError() == nil && ctx.Err() != nil {
		canceled = true
	}

	legacy := s.summary(time.Since(s.started), time.Now().UTC())
	out := AuditSummary{
		Canceled: canceled, SelectionReconciled: reconciled, EmissionCompleted: committed,
		DirectoryEmittedCount:                emitted,
		DirectoryIndeterminateEmittedCount:   indeterminateEmitted,
		DirectoryUnavailableSizeEmittedCount: unavailableEmitted,
		Backend:                              legacy.Backend,
		VisitedEntries:                       legacy.VisitedEntries,
		VisitedDirectories:                   legacy.VisitedDirectories,
		GapCount:                             legacy.GapCount,
		BoundarySkipCount:                    legacy.BoundarySkipCount,
		VanishedCount:                        legacy.VanishedCount,
		QueueLimitSkipCount:                  legacy.QueueLimitSkipCount,
		ExclusionCount:                       legacy.ExclusionCount,
		RemoteSkipCount:                      legacy.RemoteSkipCount,
		StalledCount:                         legacy.StalledCount,
		PeakDepth:                            legacy.PeakDepth,
		PeakPendingDirectories:               legacy.PeakPendingDirectories,
		DurationNanos:                        legacy.DurationNanos,
		TerminalObservedAt:                   legacy.TerminalObservedAt,
	}
	if reconciled {
		out.AuditSelectionCounts = &counts
	}
	// Worker shutdown after a fatal or output error cancels the run context
	// internally; only a caller cancellation marks roots canceled.
	external := ctx.Err() != nil && s.walkError() == nil && s.outputError() == nil
	for _, root := range legacy.Roots {
		if !external {
			root.Canceled = false
		}
		out.Roots = append(out.Roots, AuditRootSummary{
			RootID: root.RootID, Lifecycle: root.Lifecycle,
			VisitedEntries: root.VisitedEntries, VisitedDirectories: root.VisitedDirectories,
			GapCount: root.GapCount, BoundarySkipCount: root.BoundarySkipCount,
			ExclusionCount: root.ExclusionCount, VanishedCount: root.VanishedCount,
			QueueLimitSkipCount: root.QueueLimitSkipCount, RemoteSkipCount: root.RemoteSkipCount,
			StalledCount: root.StalledCount, Canceled: root.Canceled, Failed: root.Failed,
		})
	}
	fatal := s.walkError()
	switch {
	case fatal != nil:
		out.Lifecycle = LifecycleFailed
		out.Failure = auditFailure(fatal)
	case canceled || legacy.GapCount > 0:
		out.Lifecycle = LifecyclePartial
	default:
		out.Lifecycle = LifecycleComplete
	}

	if outErr := s.outputError(); outErr != nil {
		out.Lifecycle = LifecycleFailed
		return out, fmt.Errorf("emit inventory stream: %w", outErr)
	}
	if err := sink.AuditSummary(out); err != nil {
		out.Lifecycle = LifecycleFailed
		return out, fmt.Errorf("emit inventory summary: %w", err)
	}
	return out, fatal
}

func auditFailure(err error) *AuditFailure {
	var budget *AggregateBudgetError
	switch {
	case errors.As(err, &budget):
		limit, retained, attempted := budget.Limit, budget.Retained, budget.AttemptedRetained
		return &AuditFailure{
			Code: FailureAggregateBudget, Limit: &limit, Retained: &retained, AttemptedRetained: &attempted,
		}
	case errors.Is(err, errAggregateOverflow):
		return &AuditFailure{Code: FailureAggregateOverflow}
	default:
		return &AuditFailure{Code: FailureTraversal}
	}
}

// auditSelect classifies every depth-eligible aggregate against the optional
// inclusive floor, orders the selection and returns the planned top-K rows.
func (a *aggregator) auditSelect(depth, top int, basis string, floor *int64) ([]AuditDirectory, AuditSelectionCounts, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var counts AuditSelectionCounts
	if a.err != nil {
		return nil, counts, a.err
	}
	type candidate struct {
		row   AuditDirectory
		group int
		bytes int64
	}
	selected := make([]candidate, 0, len(a.values))
	for _, state := range a.values {
		if depth >= 0 && state.Depth > depth {
			continue
		}
		row := AuditDirectory{
			RootID: state.RootID, RelativePath: state.RelativePath, Depth: state.Depth,
			FileCount: state.FileCount, DescendantDirectoryCount: state.DescendantDirectoryCount,
			AllocatedUnmeasuredCount: state.AllocatedUnmeasuredCount,
			Lifecycle:                state.Lifecycle, AffectingGapCount: state.AffectingGapCount,
			Accounting: *newDirectoryAccounting(state),
		}
		claim := row.SelectedClaim(basis)
		counts.DepthEligible++
		unavailable := claim.Status == ClaimStatusUnsupported || claim.Bytes == nil
		if unavailable {
			counts.UnavailableSizeEligible++
		}
		row.FloorDecision = floorDecision(claim, floor)
		switch row.FloorDecision {
		case floorExcluded:
			counts.Excluded++
			continue
		case FloorMeets:
			counts.Meets++
		case FloorIndeterminate:
			counts.Indeterminate++
		case FloorNotApplied:
			counts.NotApplied++
		}
		c := candidate{row: row}
		c.group, c.bytes = auditRank(row, basis)
		selected = append(selected, c)
	}
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		if left.group != right.group {
			return left.group < right.group
		}
		if left.bytes != right.bytes {
			return left.bytes > right.bytes
		}
		if left.row.RootID != right.row.RootID {
			return left.row.RootID < right.row.RootID
		}
		return left.row.RelativePath < right.row.RelativePath
	})
	counts.SelectedBeforeTop = int64(len(selected))
	counts.PlannedEmission = counts.SelectedBeforeTop
	if top > 0 && int64(top) < counts.PlannedEmission {
		counts.PlannedEmission = int64(top)
	}
	counts.SelectionTruncated = counts.PlannedEmission < counts.SelectedBeforeTop
	rows := make([]AuditDirectory, counts.PlannedEmission)
	for i := range rows {
		rows[i] = selected[i].row
	}
	return rows, counts, nil
}

// floorDecision applies the inclusive floor to one selected claim. A lower
// bound at or above the floor definitely meets it; below, it is undecided.
// An unavailable plane is never treated as zero.
func floorDecision(claim AccountingClaim, floor *int64) string {
	if floor == nil {
		return FloorNotApplied
	}
	if claim.Status == ClaimStatusUnsupported || claim.Bytes == nil {
		return FloorIndeterminate
	}
	if *claim.Bytes >= *floor {
		return FloorMeets
	}
	if claim.Bound == BoundExact {
		return floorExcluded
	}
	return FloorIndeterminate
}
