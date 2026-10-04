package inventory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// ValidateAuditStream checks the cross-record semantics of a v2 directory-audit
// JSONL stream that a per-line schema cannot: one header first, one summary
// last, contiguous sequence and a single run id, known roots, unique directory
// identities, floor decisions consistent with the header, the selection count
// equations and the budget failure arithmetic. It retains only bounded state
// per emitted row identity. A stream without a terminal summary is reported as
// incomplete even when every line is individually valid.
func ValidateAuditStream(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	type envelope struct {
		Type  string          `json:"type"`
		RunID string          `json:"run_id"`
		Seq   json.Number     `json:"seq"`
		Data  json.RawMessage `json:"data"`
	}
	var (
		checker = NewAuditStreamChecker()
		runID   string
		next    int64
	)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if checker.summary != nil {
			return errors.New("record after terminal summary")
		}
		var env envelope
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&env); err != nil {
			return fmt.Errorf("record %d: %w", next, err)
		}
		seq, err := env.Seq.Int64()
		if err != nil || seq != next {
			return fmt.Errorf("record %d: sequence %s is not contiguous", next, env.Seq)
		}
		if next == 0 {
			runID = env.RunID
		} else if env.RunID != runID {
			return fmt.Errorf("record %d: run id changed", next)
		}
		next++
		data := json.NewDecoder(bytes.NewReader(env.Data))
		data.UseNumber()
		switch env.Type {
		case RecordHeaderV2:
			if seq != 0 {
				return errors.New("header must appear exactly once, first")
			}
			var header AuditHeader
			if err := data.Decode(&header); err != nil {
				return fmt.Errorf("header: %w", err)
			}
			if err := checker.Header(header); err != nil {
				return err
			}
		case RecordGap:
			var gap Gap
			if checker.header != nil {
				if err := data.Decode(&gap); err != nil {
					return fmt.Errorf("gap: %w", err)
				}
			}
			if err := checker.Gap(gap); err != nil {
				return err
			}
		case RecordDirectoryV2:
			var row AuditDirectory
			if checker.header != nil {
				if err := data.Decode(&row); err != nil {
					return fmt.Errorf("directory: %w", err)
				}
			}
			if err := checker.Directory(row); err != nil {
				return err
			}
		case RecordSummaryV2:
			var summary AuditSummary
			if checker.header != nil {
				if err := data.Decode(&summary); err != nil {
					return fmt.Errorf("summary: %w", err)
				}
			}
			if err := checker.Summary(summary); err != nil {
				return err
			}
		default:
			return fmt.Errorf("record %d: unexpected type %q in directory-audit profile", seq, env.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return checker.Finish()
}

// AuditStreamChecker applies the directory-audit stream semantics record by
// record, for a decoded stream or for records as a producer accepts them. It
// retains one identity key per emitted row and per-root counters, never the
// rows themselves. Envelope sequence and run id are the caller's concern.
type AuditStreamChecker struct {
	header      *AuditHeader
	summary     *AuditSummary
	roots       map[string]bool
	seen        map[string]bool
	rows        int64
	indetRows   int64
	unavailRows int64
	lastGroup   int
	lastBytes   int64
	lastRoot    string
	lastPath    string
	// affecting counts completeness-affecting gap records per root;
	// bounded by the number of declared roots, not by gap volume.
	affecting map[string]int64
	// decisions counts emitted rows per floor decision.
	decisions map[string]int64
	// rowGaps is the largest affecting_gap_count on any emitted row per
	// root; a row cannot carry more affecting gaps than its root emitted.
	rowGaps map[string]int64
}

// NewAuditStreamChecker returns a checker awaiting the header record.
func NewAuditStreamChecker() *AuditStreamChecker {
	return &AuditStreamChecker{
		roots: map[string]bool{}, seen: map[string]bool{}, lastGroup: -1,
		affecting: map[string]int64{}, decisions: map[string]int64{}, rowGaps: map[string]int64{},
	}
}

// Header accepts the single leading header.v2 record.
func (c *AuditStreamChecker) Header(header AuditHeader) error {
	if c.header != nil {
		return errors.New("header must appear exactly once, first")
	}
	if header.DirectorySelection.FloorApplied != (header.DirectorySelection.FloorBytes != nil) {
		return errors.New("header: floor_bytes must be present iff floor_applied")
	}
	for _, root := range header.Roots {
		if c.roots[root.ID] {
			return fmt.Errorf("header: duplicate root id %q", root.ID)
		}
		c.roots[root.ID] = true
	}
	c.header = &header
	return nil
}

// Gap accepts one gap.v1 record.
func (c *AuditStreamChecker) Gap(gap Gap) error {
	if err := c.open("gap"); err != nil {
		return err
	}
	if !c.roots[gap.RootID] {
		return fmt.Errorf("gap references unknown root %q", gap.RootID)
	}
	if gap.AffectsCompleteness {
		c.affecting[gap.RootID]++
	}
	return nil
}

// Directory accepts one directory.v2 row in emission order.
func (c *AuditStreamChecker) Directory(row AuditDirectory) error {
	if err := c.open("directory"); err != nil {
		return err
	}
	if !c.roots[row.RootID] {
		return fmt.Errorf("directory references unknown root %q", row.RootID)
	}
	if err := checkAuditRow(*c.header, row); err != nil {
		return err
	}
	if row.AffectingGapCount > c.rowGaps[row.RootID] {
		c.rowGaps[row.RootID] = row.AffectingGapCount
	}
	key := row.RootID + "\x00" + row.RelativePath
	if c.seen[key] {
		return fmt.Errorf("duplicate directory identity %q/%q", row.RootID, row.RelativePath)
	}
	c.seen[key] = true
	group, bytesValue := auditRank(row, c.header.DirectorySelection.SizeBasis)
	if c.rows > 0 && auditRankBefore(group, bytesValue, row.RootID, row.RelativePath,
		c.lastGroup, c.lastBytes, c.lastRoot, c.lastPath) {
		return fmt.Errorf("directory %q is out of documented order", row.RelativePath)
	}
	c.lastGroup, c.lastBytes, c.lastRoot, c.lastPath = group, bytesValue, row.RootID, row.RelativePath
	c.rows++
	c.decisions[row.FloorDecision]++
	if row.FloorDecision == FloorIndeterminate {
		c.indetRows++
	}
	if row.SelectedClaim(c.header.DirectorySelection.SizeBasis).Status == ClaimStatusUnsupported {
		c.unavailRows++
	}
	return nil
}

// Summary accepts the terminal summary.v2 record and reconciles the stream.
func (c *AuditStreamChecker) Summary(summary AuditSummary) error {
	if err := c.open("summary"); err != nil {
		return err
	}
	c.summary = &summary
	if err := checkAuditSummary(*c.header, summary, c.rows, c.indetRows, c.unavailRows,
		c.roots, c.affecting, c.decisions); err != nil {
		return err
	}
	for rootID, n := range c.rowGaps {
		if n > c.affecting[rootID] {
			return fmt.Errorf("directory rows under root %q claim %d affecting gaps, root emitted %d",
				rootID, n, c.affecting[rootID])
		}
	}
	return nil
}

// Finish reports a stream that ended without its header or terminal summary.
func (c *AuditStreamChecker) Finish() error {
	if c.header == nil {
		return errors.New("stream has no header")
	}
	if c.summary == nil {
		return errors.New("stream is incomplete: no terminal summary")
	}
	return nil
}

func (c *AuditStreamChecker) open(record string) error {
	if c.header == nil {
		return fmt.Errorf("%s before header", record)
	}
	if c.summary != nil {
		return errors.New("record after terminal summary")
	}
	return nil
}

func checkAuditRow(header AuditHeader, row AuditDirectory) error {
	sel := header.DirectorySelection
	if err := checkAuditRelativePath(row.RelativePath); err != nil {
		return err
	}
	if err := checkAuditRowEvidence(row); err != nil {
		return err
	}
	if sel.Depth >= 0 && row.Depth > sel.Depth {
		return fmt.Errorf("directory %q exceeds declared depth", row.RelativePath)
	}
	if row.Depth != relativeDepth(row.RelativePath) {
		return fmt.Errorf("directory %q depth does not match its relative path", row.RelativePath)
	}
	if row.Accounting.Apparent.Basis != BasisApparentPathEntrySum {
		return fmt.Errorf("directory %q: apparent claim has basis %q", row.RelativePath, row.Accounting.Apparent.Basis)
	}
	if allocated := row.Accounting.Allocated; allocated.Status != ClaimStatusUnsupported &&
		allocated.Basis != BasisAllocatedPathEntrySum {
		return fmt.Errorf("directory %q: allocated claim has basis %q", row.RelativePath, allocated.Basis)
	}
	want := floorDecision(row.SelectedClaim(sel.SizeBasis), sel.FloorBytes)
	if !sel.FloorApplied {
		want = FloorNotApplied
	}
	if want == floorExcluded {
		return fmt.Errorf("directory %q is below the floor but was emitted", row.RelativePath)
	}
	if row.FloorDecision != want {
		return fmt.Errorf("directory %q floor decision %q, want %q", row.RelativePath, row.FloorDecision, want)
	}
	return nil
}

// checkAuditRelativePath requires the canonical root-relative identity the
// producer emits: "." for the root, otherwise slash-delimited segments with no
// leading slash and no empty, "." or ".." segment. A literal backslash is an
// ordinary POSIX filename byte and is accepted; consumers must not reinterpret
// it as a separator.
func checkAuditRelativePath(rel string) error {
	if rel == "." {
		return nil
	}
	if rel == "" || strings.ContainsRune(rel, 0) {
		return fmt.Errorf("directory relative path %q is not canonical", rel)
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("directory relative path %q is not canonical", rel)
		}
	}
	return nil
}

// checkAuditRowEvidence ties a row's traversal lifecycle, its affecting gap
// count and its apparent claim together: partial coverage has affecting gaps
// and a lower bound; complete coverage has none and an exact value. The
// allocated plane carries its own uncertainty, so a complete row may report a
// lower or unsupported allocated claim, but a partial row cannot report an
// exact one.
func checkAuditRowEvidence(row AuditDirectory) error {
	apparent, allocated := row.Accounting.Apparent, row.Accounting.Allocated
	if apparent.Bytes == nil {
		return fmt.Errorf("directory %q has no apparent bytes", row.RelativePath)
	}
	switch row.Lifecycle {
	case LifecycleComplete:
		if row.AffectingGapCount != 0 || apparent.Status != ClaimStatusMeasured || apparent.Bound != BoundExact {
			return fmt.Errorf("directory %q is complete but carries gaps or a non-exact apparent claim", row.RelativePath)
		}
	case LifecyclePartial:
		if row.AffectingGapCount <= 0 || apparent.Status != ClaimStatusPartial || apparent.Bound != BoundLower {
			return fmt.Errorf("directory %q is partial without affecting gaps and a lower apparent bound", row.RelativePath)
		}
		if allocated.Status == ClaimStatusMeasured || allocated.Bound == BoundExact {
			return fmt.Errorf("directory %q is partial but reports an exact allocated claim", row.RelativePath)
		}
	default:
		return fmt.Errorf("directory %q has lifecycle %q", row.RelativePath, row.Lifecycle)
	}
	// Allocation uncertainty is counted in files: at most every covered file
	// is unmeasured, and the typed claim carries the same count.
	unmeasured := row.AllocatedUnmeasuredCount
	if row.FileCount < 0 || unmeasured < 0 || unmeasured > row.FileCount {
		return fmt.Errorf("directory %q: allocated_unmeasured_count %d is outside 0..file_count %d",
			row.RelativePath, unmeasured, row.FileCount)
	}
	switch allocated.Status {
	case ClaimStatusUnsupported:
		if allocated.Bytes != nil || allocated.UnmeasuredCount != nil {
			return fmt.Errorf("directory %q: unsupported allocated claim carries bytes or counts", row.RelativePath)
		}
		if row.FileCount == 0 || unmeasured != row.FileCount {
			return fmt.Errorf("directory %q: unsupported allocated claim requires every covered file unmeasured", row.RelativePath)
		}
	case ClaimStatusMeasured:
		if allocated.Bound != BoundExact || allocated.Bytes == nil || unmeasured != 0 || allocated.UnmeasuredCount != nil {
			return fmt.Errorf("directory %q: measured allocated claim is not exact and fully observed", row.RelativePath)
		}
	case ClaimStatusPartial:
		if allocated.Bound != BoundLower || allocated.Bytes == nil {
			return fmt.Errorf("directory %q: partial allocated claim is not a lower bound", row.RelativePath)
		}
		switch {
		case allocated.UnmeasuredCount == nil:
			// Lower only because traversal coverage is partial.
			if unmeasured != 0 || row.Lifecycle != LifecyclePartial {
				return fmt.Errorf("directory %q: partial allocated claim omits its unmeasured count", row.RelativePath)
			}
		case *allocated.UnmeasuredCount != unmeasured || unmeasured == 0 || unmeasured >= row.FileCount:
			return fmt.Errorf("directory %q: allocated unmeasured_count does not reconcile with %d of %d files",
				row.RelativePath, unmeasured, row.FileCount)
		}
	default:
		return fmt.Errorf("directory %q: allocated claim status %q", row.RelativePath, allocated.Status)
	}
	return nil
}

func checkAuditSummary(header AuditHeader, s AuditSummary, rows, indet, unavail int64,
	roots map[string]bool, affecting, decisions map[string]int64) error {
	if s.DirectoryEmittedCount != rows || s.DirectoryIndeterminateEmittedCount != indet ||
		s.DirectoryUnavailableSizeEmittedCount != unavail {
		return fmt.Errorf("summary emitted counts %d/%d/%d do not match records %d/%d/%d",
			s.DirectoryEmittedCount, s.DirectoryIndeterminateEmittedCount,
			s.DirectoryUnavailableSizeEmittedCount, rows, indet, unavail)
	}
	if len(s.Roots) != len(roots) {
		return fmt.Errorf("summary has %d root rows for %d declared roots", len(s.Roots), len(roots))
	}
	var entries, dirs, gaps, emittedGaps int64
	var boundary, exclusion, vanished, queueLimit, remote, stalled int64
	for _, n := range affecting {
		emittedGaps += n
	}
	if s.GapCount != emittedGaps {
		return fmt.Errorf("summary gap_count %d does not match %d affecting gap records", s.GapCount, emittedGaps)
	}
	summaryRoots := make(map[string]bool, len(s.Roots))
	for _, root := range s.Roots {
		if !roots[root.RootID] {
			return fmt.Errorf("summary references unknown root %q", root.RootID)
		}
		if summaryRoots[root.RootID] {
			return fmt.Errorf("summary repeats root %q", root.RootID)
		}
		summaryRoots[root.RootID] = true
		if root.GapCount != affecting[root.RootID] {
			return fmt.Errorf("root %q gap_count %d does not match %d affecting gap records",
				root.RootID, root.GapCount, affecting[root.RootID])
		}
		if err := checkAuditRootEvidence(root, s.Lifecycle, s.Canceled); err != nil {
			return err
		}
		for _, add := range []struct {
			dst   *int64
			value int64
		}{
			{&gaps, root.GapCount}, {&entries, root.VisitedEntries}, {&dirs, root.VisitedDirectories},
			{&boundary, root.BoundarySkipCount}, {&exclusion, root.ExclusionCount},
			{&vanished, root.VanishedCount}, {&queueLimit, root.QueueLimitSkipCount},
			{&remote, root.RemoteSkipCount}, {&stalled, root.StalledCount},
		} {
			if !checkedAdd(add.dst, add.value) {
				return fmt.Errorf("root %q evidence counters are negative or overflow", root.RootID)
			}
		}
	}
	if gaps != s.GapCount {
		return errors.New("summary gap_count does not reconcile with per-root evidence")
	}
	if entries != s.VisitedEntries || dirs != s.VisitedDirectories {
		return errors.New("summary visit totals do not reconcile with per-root evidence")
	}
	if boundary != s.BoundarySkipCount || exclusion != s.ExclusionCount || vanished != s.VanishedCount ||
		queueLimit != s.QueueLimitSkipCount || remote != s.RemoteSkipCount || stalled != s.StalledCount {
		return errors.New("summary traversal counters do not reconcile with per-root evidence")
	}
	switch s.Lifecycle {
	case LifecycleComplete:
		if !s.SelectionReconciled || !s.EmissionCompleted || s.Canceled || s.GapCount != 0 {
			return errors.New("complete audit requires reconciled selection, completed emission, no cancellation and no affecting gaps")
		}
	case LifecyclePartial:
		if !s.Canceled && s.GapCount == 0 {
			return errors.New("partial audit requires cancellation or affecting gaps")
		}
		if !s.Canceled && !s.EmissionCompleted {
			return errors.New("incomplete emission without cancellation must be a failed audit")
		}
	}
	if s.Failure != nil && s.Failure.Code == FailureAggregateBudget {
		if s.Failure.Limit == nil || s.Failure.Retained == nil || s.Failure.AttemptedRetained == nil {
			return errors.New("budget failure requires limit, retained and attempted_retained")
		}
		if *s.Failure.Limit != int64(header.MaxAggregateDirectories) {
			return fmt.Errorf("budget failure limit %d does not match header max_aggregate_directories %d",
				*s.Failure.Limit, header.MaxAggregateDirectories)
		}
		if *s.Failure.Retained != *s.Failure.Limit || *s.Failure.Retained == math.MaxInt64 ||
			*s.Failure.AttemptedRetained != *s.Failure.Retained+1 {
			return errors.New("budget failure violates retained = limit, attempted_retained = retained + 1")
		}
	}
	c := s.AuditSelectionCounts
	if !s.SelectionReconciled {
		if c != nil {
			return errors.New("unreconciled selection must omit selection counts")
		}
		if rows != 0 || s.EmissionCompleted {
			return errors.New("unreconciled selection cannot emit rows or complete emission")
		}
		return nil
	}
	if c == nil {
		return errors.New("reconciled selection requires selection counts")
	}
	sel := header.DirectorySelection
	if c.DepthEligible != c.Meets+c.Indeterminate+c.Excluded+c.NotApplied {
		return errors.New("selection counts violate E = M + I + X + U")
	}
	if c.SelectedBeforeTop != c.DepthEligible-c.Excluded {
		return errors.New("selection counts violate S = E - X")
	}
	if sel.FloorApplied && c.NotApplied != 0 {
		return errors.New("applied floor requires U = 0")
	}
	if !sel.FloorApplied && (c.Meets != 0 || c.Indeterminate != 0 || c.Excluded != 0 || c.NotApplied != c.DepthEligible) {
		return errors.New("omitted floor requires M = I = X = 0 and U = E")
	}
	wantK := c.SelectedBeforeTop
	if sel.Top > 0 && int64(sel.Top) < wantK {
		wantK = int64(sel.Top)
	}
	if c.PlannedEmission != wantK {
		return fmt.Errorf("planned emission %d, want %d", c.PlannedEmission, wantK)
	}
	if c.SelectionTruncated != (c.PlannedEmission < c.SelectedBeforeTop) {
		return errors.New("directory_selection_truncated must mean K < S")
	}
	// Emitted rows per floor decision are drawn from the reconciled
	// populations; a complete, untruncated emission must equal them exactly.
	populations := map[string]int64{
		FloorMeets: c.Meets, FloorIndeterminate: c.Indeterminate, FloorNotApplied: c.NotApplied,
	}
	for decision, population := range populations {
		if decisions[decision] > population {
			return fmt.Errorf("%d emitted %s rows exceed the reconciled population %d",
				decisions[decision], decision, population)
		}
		if rows == c.SelectedBeforeTop && decisions[decision] != population {
			return fmt.Errorf("full emission has %d %s rows, summary reports %d",
				decisions[decision], decision, population)
		}
	}
	// An unavailable size is never excluded, so it is drawn from I + U; on a
	// full emission every one of them was emitted.
	if c.UnavailableSizeEligible < 0 || c.UnavailableSizeEligible > c.Indeterminate+c.NotApplied ||
		unavail > c.UnavailableSizeEligible {
		return errors.New("unavailable-size counts are inconsistent")
	}
	if rows == c.SelectedBeforeTop && unavail != c.UnavailableSizeEligible {
		return fmt.Errorf("full emission has %d unavailable-size rows, summary reports %d",
			unavail, c.UnavailableSizeEligible)
	}
	if rows > c.PlannedEmission {
		return errors.New("emitted more rows than planned")
	}
	if s.EmissionCompleted != (rows == c.PlannedEmission) && !s.Canceled {
		return errors.New("emission_completed must mean D = K")
	}
	if s.EmissionCompleted && rows != c.PlannedEmission {
		return errors.New("complete emission requires D = K")
	}
	return nil
}

// checkAuditRootEvidence keeps per-root traversal evidence consistent with
// itself and with the audit result. Output-only cancellation leaves completed
// roots complete; a complete audit cannot hide an incomplete root; a canceled
// root implies a canceled audit and is never also failed.
func checkAuditRootEvidence(root AuditRootSummary, auditLifecycle string, auditCanceled bool) error {
	// Only caller cancellation marks a root canceled; a fatal or output error
	// stops workers internally and keeps failure semantics.
	if root.Canceled && (root.Failed || !auditCanceled) {
		return fmt.Errorf("root %q is canceled but the root failed or the audit is not canceled", root.RootID)
	}
	switch root.Lifecycle {
	case LifecycleComplete:
		if root.GapCount != 0 || root.Canceled || root.Failed {
			return fmt.Errorf("root %q is complete but carries gaps, cancellation or failure", root.RootID)
		}
	case LifecyclePartial:
		if root.GapCount == 0 && !root.Canceled {
			return fmt.Errorf("root %q is partial without affecting gaps or cancellation", root.RootID)
		}
		if root.Failed {
			return fmt.Errorf("root %q is partial but flagged failed", root.RootID)
		}
	case LifecycleFailed:
		if !root.Failed {
			return fmt.Errorf("root %q lifecycle failed without failed flag", root.RootID)
		}
	}
	if root.Failed && (root.Lifecycle != LifecycleFailed || auditLifecycle != LifecycleFailed) {
		return fmt.Errorf("root %q failed but the audit or root lifecycle is not failed", root.RootID)
	}
	if auditLifecycle == LifecycleComplete && root.Lifecycle != LifecycleComplete {
		return fmt.Errorf("complete audit hides %s root %q", root.Lifecycle, root.RootID)
	}
	return nil
}

// auditRankBefore reports whether (g, b, root, path) must precede the prior
// row under directory_audit_rank_v1, i.e. the stream is out of order.
func auditRankBefore(g int, b int64, root, path string, pg int, pb int64, proot, ppath string) bool {
	if g != pg {
		return g < pg
	}
	if g != 3 && b != pb {
		return b > pb
	}
	if root != proot {
		return root < proot
	}
	return path < ppath
}

// auditRank returns the ordering group and comparable bytes for one row.
func auditRank(row AuditDirectory, basis string) (int, int64) {
	claim := row.SelectedClaim(basis)
	switch {
	case claim.Status == ClaimStatusUnsupported || claim.Bytes == nil:
		return 3, 0
	case claim.Bound == BoundExact:
		return 0, *claim.Bytes
	case row.FloorDecision == FloorIndeterminate:
		return 2, *claim.Bytes
	default:
		return 1, *claim.Bytes
	}
}
