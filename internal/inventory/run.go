package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// Run validates the declared scope, emits exactly one header, performs the
// read-only enumeration, and emits one terminal summary when the sink remains
// writable.
func Run(ctx context.Context, opts Options, sink Sink) (Summary, error) {
	if sink == nil {
		return Summary{}, errors.New("inventory sink is required")
	}
	nopts, err := normalizeOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	if nopts.EmissionMode == EmissionDirectorySummary {
		if _, ok := sink.(DirectorySink); !ok {
			return Summary{}, errors.New("directory-summary sink support is required")
		}
	}
	if nopts.EmissionMode == EmissionDirectoryAudit {
		if _, ok := sink.(AuditSink); !ok {
			return Summary{}, errors.New("directory-audit sink support is required")
		}
	}
	if nopts.OneFilesystem {
		for _, root := range nopts.roots {
			if root.DeviceID == "" {
				return Summary{}, fmt.Errorf(
					"one-filesystem cannot be confirmed for root %s on this platform", root.Path)
			}
		}
	}

	started := time.Now()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := newRunState(runCtx, cancel, nopts, sink)
	state.started = started

	profile := ProfileV0
	var subject *AggregateSubject
	var directoryDepth, directoryTop, maxAggregateDirectories *int
	var directoryAccounting *bool
	if nopts.EmissionMode == EmissionDirectorySummary {
		profile = AggregationProfileV0
		if nopts.DirectoryAccounting {
			profile = AggregationProfileV1
			directoryAccounting = &nopts.DirectoryAccounting
		}
		subject = &AggregateSubject{
			AccountingScope: "full_subject", EntryType: "file",
			SymlinkPosture: "not_followed", RankingSizeBasis: nopts.SizeBasis,
		}
		directoryDepth = &nopts.DirectoryDepth
		directoryTop = &nopts.DirectoryTop
		maxAggregateDirectories = &nopts.MaxAggregateDirectories
	}
	header := Header{
		RunID:                   nopts.RunID,
		CapturedAt:              nopts.now,
		Profile:                 profile,
		Roots:                   append([]Root(nil), nopts.roots...),
		Backend:                 nopts.Backend,
		Workers:                 nopts.Workers,
		MaxOpenDirs:             nopts.MaxOpenDirs,
		MaxPendingDirs:          nopts.MaxPendingDirs,
		OneFilesystem:           nopts.OneFilesystem,
		Top:                     nopts.Top,
		EmissionMode:            nopts.EmissionMode,
		DirectoryDepth:          directoryDepth,
		DirectoryTop:            directoryTop,
		MaxAggregateDirectories: maxAggregateDirectories,
		DirectoryAccounting:     directoryAccounting,
		Subject:                 subject,
		Exclusions:              append([]Exclusion{}, nopts.exclusions...),
		Ordering:                inventoryOrdering(nopts.EmissionMode),
		PathProtection:          PathProtectionSourceStructure,
		MutationContract:        nopts.MutationContract,
		Filters: FilterSummary{
			EntryType:      "file",
			MinSizeBytes:   nopts.MinSize,
			SizeBasis:      nopts.SizeBasis,
			OlderThanNanos: int64(nopts.OlderThan),
			NewerThanNanos: int64(nopts.NewerThan),
			CapturedAt:     nopts.now.UTC().Format(time.RFC3339Nano),
		},
	}
	if nopts.Traversal != nil {
		header.Traversal = &TraversalScope{
			MaxDepth: nopts.Traversal.MaxDepth, DiscoveredRoots: nopts.Traversal.DiscoveredRoots,
			DirectoryPruning: nopts.Traversal.OnDirectory != nil,
		}
	}
	audit := nopts.EmissionMode == EmissionDirectoryAudit
	var headerErr error
	if audit {
		headerErr = sink.(AuditSink).AuditHeader(auditHeader(nopts))
	} else {
		headerErr = sink.Header(header)
	}
	if headerErr != nil {
		return Summary{Lifecycle: LifecycleFailed, Backend: nopts.Backend},
			fmt.Errorf("emit inventory header: %w", headerErr)
	}

	var walkErr error
	switch nopts.Backend {
	case BackendSerial:
		walkErr = serialBackend{}.walk(runCtx, nopts, state)
	case BackendParallel:
		walkErr = parallelBackend{}.walk(runCtx, nopts, state)
	default:
		walkErr = fmt.Errorf("internal error: normalized backend %q", nopts.Backend)
	}

	// An external cancellation is a coverage gap. A sink failure also cancels
	// the walker, but attempting another record on that sink would only obscure
	// the original output error.
	if ctx.Err() != nil && state.outputError() == nil {
		state.addCancellationGaps()
	}
	// Parent cancellation (including deadline expiry) is represented by the
	// per-root canceled gaps above. Only a walker failure independent of the
	// caller's context makes the run failed.
	if walkErr != nil && ctx.Err() == nil && !errors.Is(walkErr, context.Canceled) {
		state.setWalkError(walkErr)
	}

	if audit {
		// The audit terminal state is carried by the typed AuditSummary; the
		// legacy Summary return keeps callers' lifecycle/exit handling.
		result, err := state.completeAudit(ctx, sink.(AuditSink))
		return Summary{Lifecycle: result.Lifecycle, Backend: result.Backend, Canceled: result.Canceled}, err
	}

	state.flushTop()
	state.flushDirectories()
	summary := state.summary(time.Since(started), time.Now().UTC())

	if outErr := state.outputError(); outErr != nil {
		return summary, fmt.Errorf("emit inventory stream: %w", outErr)
	}
	if err := sink.Summary(summary); err != nil {
		summary.Lifecycle = LifecycleFailed
		return summary, fmt.Errorf("emit inventory summary: %w", err)
	}
	if fatal := state.walkError(); fatal != nil {
		return summary, fatal
	}
	return summary, nil
}

func inventoryOrdering(emissionMode string) string {
	if emissionMode == EmissionDirectorySummary {
		return "size_desc_root_path"
	}
	return "not_promised"
}

type runState struct {
	ctx    context.Context
	cancel context.CancelFunc
	opts   normalizedOptions
	sink   Sink

	mu           sync.Mutex
	emitMu       sync.Mutex
	progressMu   sync.Mutex
	summaryState Summary
	rootState    map[string]*RootSummary
	rootOrder    []string
	activeRoot   string
	remote       *remoteClassifier
	stalls       *stallWatch
	completed    map[string]bool
	top          *topHeap
	aggregator   *aggregator
	outErr       error
	fatal        error
	started      time.Time
	lastProgress time.Time
}

func newRunState(
	ctx context.Context,
	cancel context.CancelFunc,
	opts normalizedOptions,
	sink Sink,
) *runState {
	rootState := make(map[string]*RootSummary, len(opts.roots))
	rootOrder := make([]string, 0, len(opts.roots))
	for _, root := range opts.roots {
		rootState[root.ID] = &RootSummary{RootID: root.ID}
		if opts.Traversal != nil {
			observed := false
			rootState[root.ID].Observed = &observed
		}
		rootOrder = append(rootOrder, root.ID)
	}
	s := &runState{
		ctx: ctx, cancel: cancel, opts: opts, sink: sink,
		summaryState: Summary{
			Backend: opts.Backend, Top: opts.Top, EmissionMode: opts.EmissionMode,
			EntriesSuppressed: opts.EmissionMode != EmissionEntries,
		},
		rootState: rootState, rootOrder: rootOrder, completed: make(map[string]bool, len(opts.roots)),
		remote: newRemoteClassifier(),
		stalls: &stallWatch{onAlert: opts.OnStall, timeout: max(opts.stallTimeout, 0), alertAge: opts.alertAfter},
	}
	if opts.Top > 0 {
		s.top = newTopHeap(opts.Top, opts.SizeBasis)
	}
	if opts.EmissionMode == EmissionDirectoryAudit {
		s.aggregator = newAggregator(opts.MaxAggregateDirectories, true)
	}
	if opts.EmissionMode == EmissionDirectorySummary {
		s.aggregator = newAggregator(opts.MaxAggregateDirectories, opts.DirectoryAccounting)
		emitted := int64(0)
		truncated := false
		s.summaryState.DirectoryEmittedCount = &emitted
		s.summaryState.DirectorySelectionTruncated = &truncated
	}
	return s
}

func (s *runState) visitEntry(root Root) {
	s.mu.Lock()
	s.summaryState.VisitedEntries++
	if account := s.rootState[root.ID]; account != nil {
		account.VisitedEntries++
	}
	s.mu.Unlock()
	s.progress()
}

func (s *runState) visitDirectory(root Root, path string) {
	if s.aggregator != nil {
		if err := s.aggregator.admit(root, path); err != nil {
			s.failWalk(err)
			return
		}
	}
	s.mu.Lock()
	s.summaryState.VisitedDirectories++
	if account := s.rootState[root.ID]; account != nil {
		account.VisitedDirectories++
	}
	depth := int64(0)
	if rel := relative(root, path); rel != "" && rel != "." {
		depth = int64(strings.Count(rel, "/") + 1)
	}
	if depth > s.summaryState.PeakDepth {
		s.summaryState.PeakDepth = depth
	}
	s.mu.Unlock()
	s.progress()
}

func (s *runState) consider(root Root, path string, info fs.FileInfo) {
	if s.ctx.Err() != nil {
		return
	}
	meta := fileMetadataOf(info)
	allocated := meta.allocated
	if s.aggregator != nil {
		if err := s.aggregator.addFile(root, path, info.Size(), allocated); err != nil {
			s.failWalk(err)
			return
		}
	}
	if s.opts.EmissionMode == EmissionDirectoryAudit {
		// The audit has no file predicate. Unavailable allocation is carried
		// by typed directory claims, never as a per-file traversal gap.
		return
	}
	entry := Entry{
		RootID:             root.ID,
		RelativePath:       relative(root, path),
		LocalAbsolutePath:  path,
		EntryType:          "file",
		ApparentSizeBytes:  info.Size(),
		AllocatedSizeBytes: allocated,
		ModifiedAt:         info.ModTime().UTC(),
		ObservedAt:         time.Now().UTC(),
		DeviceID:           meta.deviceID,
		FileID:             meta.fileID,
		MetadataSource:     "lstat",
		SymlinkFollowed:    false,
	}

	selectedSize, measured := entry.SelectedSize(s.opts.SizeBasis)
	if !measured {
		s.addGap(Gap{
			RootID:              root.ID,
			RelativePath:        entry.RelativePath,
			LocalAbsolutePath:   path,
			Kind:                "metadata-unavailable",
			Detail:              "allocated size is unavailable; candidate could not be evaluated",
			AffectsCompleteness: true,
		})
		return
	}
	if selectedSize < s.opts.MinSize {
		return
	}
	age := s.opts.now.Sub(info.ModTime())
	if s.opts.OlderThan > 0 && age < s.opts.OlderThan {
		return
	}
	if s.opts.NewerThan > 0 && age > s.opts.NewerThan {
		return
	}

	s.mu.Lock()
	if s.summaryState.TimeToFirstMatchNanos == 0 {
		s.summaryState.TimeToFirstMatchNanos = int64(time.Since(s.started))
	}
	s.summaryState.MatchedCount++
	s.summaryState.MatchedApparentBytes += entry.ApparentSizeBytes
	account := s.rootState[root.ID]
	if account != nil {
		account.MatchedCount++
		account.MatchedApparentBytes += entry.ApparentSizeBytes
	}
	if allocated == nil {
		s.summaryState.AllocatedUnmeasuredCount++
		if account != nil {
			account.AllocatedUnmeasuredCount++
		}
	} else {
		s.summaryState.MatchedAllocatedBytes += *allocated
		if account != nil {
			account.MatchedAllocatedBytes += *allocated
		}
	}
	summaryOnly := s.opts.EmissionMode != EmissionEntries
	if s.top != nil {
		if summaryOnly {
			s.mu.Unlock()
			s.progress()
			return
		}
		s.top.add(entry)
		s.mu.Unlock()
		s.progress()
		return
	}
	s.mu.Unlock()
	if summaryOnly {
		s.progress()
		return
	}
	s.emitEntry(entry)
	s.progress()
}

func (s *runState) emitEntry(entry Entry) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.outputError() != nil {
		return
	}
	if err := s.sink.Entry(entry); err != nil {
		s.setOutputError(err)
		return
	}
	s.mu.Lock()
	s.summaryState.EmittedCount++
	s.summaryState.EmittedApparentBytes += entry.ApparentSizeBytes
	if account := s.rootState[entry.RootID]; account != nil {
		account.EmittedCount++
		account.EmittedApparentBytes += entry.ApparentSizeBytes
	}
	if entry.AllocatedSizeBytes == nil {
		s.summaryState.EmittedUnmeasuredCount++
		if account := s.rootState[entry.RootID]; account != nil {
			account.EmittedUnmeasuredCount++
		}
	} else {
		s.summaryState.EmittedAllocatedBytes += *entry.AllocatedSizeBytes
		if account := s.rootState[entry.RootID]; account != nil {
			account.EmittedAllocatedBytes += *entry.AllocatedSizeBytes
		}
	}
	s.mu.Unlock()
}

func (s *runState) addGap(gap Gap) {
	s.mu.Lock()
	if gap.RootID == "" {
		gap.RootID = s.activeRoot
	}
	if s.aggregator != nil && gap.AffectsCompleteness {
		if root, ok := s.rootByID(gap.RootID); ok {
			path := gap.LocalAbsolutePath
			if path == "" {
				path = root.Path
			}
			if err := s.aggregator.addGap(root, path); err != nil {
				s.mu.Unlock()
				s.failWalk(err)
				return
			}
		}
	}
	gap.Detail = sanitizeGapDetail(gap.Detail)
	if gap.Kind == gapKindStalled {
		// The aggregator above used the real path so only the affected
		// directories turn partial; the emitted record is path-free because
		// the blocked directory may itself be sensitive.
		gap.RelativePath = ""
		gap.LocalAbsolutePath = ""
		s.summaryState.StalledCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.StalledCount++
		}
	}
	if gap.Kind == gapKindRemote {
		s.summaryState.RemoteSkipCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.RemoteSkipCount++
		}
	}
	if gap.Kind == "mount-boundary" && !gap.AffectsCompleteness {
		s.summaryState.BoundarySkipCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.BoundarySkipCount++
		}
	} else if gap.AffectsCompleteness {
		s.summaryState.GapCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.GapCount++
		}
	}
	if gap.Kind == "vanished" {
		s.summaryState.VanishedCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.VanishedCount++
		}
	}
	if gap.Kind == "directory-queue-limit" {
		s.summaryState.QueueLimitSkipCount++
		if account := s.rootState[gap.RootID]; account != nil {
			account.QueueLimitSkipCount++
		}
	}
	s.mu.Unlock()
	s.progress()

	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.outputError() != nil {
		return
	}
	if err := s.sink.Gap(gap); err != nil {
		s.setOutputError(err)
	}
}

func (s *runState) flushDirectories() {
	if s.aggregator == nil || s.walkError() != nil || s.outputError() != nil || s.ctx.Err() != nil {
		return
	}
	records, err := s.aggregator.records(s.opts.DirectoryDepth, s.opts.DirectoryTop, s.opts.SizeBasis)
	if err != nil {
		s.failWalk(err)
		return
	}
	sink := s.sink.(DirectorySink)
	totalEligible := s.aggregator.eligibleCount(s.opts.DirectoryDepth)
	for _, record := range records {
		s.emitMu.Lock()
		err := sink.Directory(record)
		s.emitMu.Unlock()
		if err != nil {
			s.setOutputError(err)
			return
		}
		s.mu.Lock()
		*s.summaryState.DirectoryEmittedCount++
		s.mu.Unlock()
	}
	s.mu.Lock()
	*s.summaryState.DirectorySelectionTruncated = s.opts.DirectoryTop > 0 && totalEligible > len(records)
	s.mu.Unlock()
}

func (s *runState) rootByID(id string) (Root, bool) {
	for _, root := range s.opts.roots {
		if root.ID == id {
			return root, true
		}
	}
	return Root{}, false
}

func (s *runState) observePeakPending(value int64) {
	s.mu.Lock()
	if value > s.summaryState.PeakPendingDirectories {
		s.summaryState.PeakPendingDirectories = value
	}
	s.mu.Unlock()
}

func (s *runState) flushTop() {
	if s.opts.EmissionMode == EmissionSummaryOnly {
		return
	}
	s.mu.Lock()
	if s.top == nil {
		s.mu.Unlock()
		return
	}
	entries := s.top.entries()
	s.mu.Unlock()
	for _, entry := range entries {
		if s.ctx.Err() != nil && s.outputError() != nil {
			return
		}
		s.emitEntry(entry)
	}
}

func (s *runState) summary(elapsed time.Duration, terminalObservedAt time.Time) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.summaryState
	out.DurationNanos = int64(elapsed)
	out.TerminalObservedAt = terminalObservedAt.UTC()
	selectedCount := out.MatchedCount
	if out.Top > 0 && selectedCount > int64(out.Top) {
		selectedCount = int64(out.Top)
	}
	out.SelectionTruncated = out.Top > 0 && out.MatchedCount > selectedCount
	out.Canceled = s.ctx.Err() != nil && s.outErr == nil && s.hasUncompletedRootLocked()
	out.Roots = make([]RootSummary, 0, len(s.rootOrder))
	switch {
	case s.outErr != nil || s.fatal != nil:
		out.Lifecycle = LifecycleFailed
	case out.GapCount > 0 || out.Canceled:
		out.Lifecycle = LifecyclePartial
	default:
		out.Lifecycle = LifecycleComplete
	}
	for _, rootID := range s.rootOrder {
		account := *s.rootState[rootID]
		account.Canceled = out.Canceled && !s.completed[rootID]
		// A failure stops the active/uncompleted portion of the walk. Roots
		// already reconciled before that point remain complete evidence.
		account.Failed = (s.outErr != nil || s.fatal != nil) && !s.completed[rootID]
		switch {
		case account.Failed:
			account.Lifecycle = LifecycleFailed
		case account.GapCount > 0 || account.Canceled || !s.completed[rootID]:
			account.Lifecycle = LifecyclePartial
		default:
			account.Lifecycle = LifecycleComplete
		}
		out.Roots = append(out.Roots, account)
	}
	return out
}

func (s *runState) addCancellationGaps() {
	s.mu.Lock()
	rootIDs := make([]string, 0, len(s.rootOrder))
	for _, rootID := range s.rootOrder {
		if !s.completed[rootID] {
			rootIDs = append(rootIDs, rootID)
		}
	}
	s.mu.Unlock()
	for _, rootID := range rootIDs {
		s.addGap(Gap{
			RootID: rootID, Kind: "canceled", Detail: "enumeration canceled",
			AffectsCompleteness: true,
		})
	}
}

func (s *runState) hasUncompletedRootLocked() bool {
	for _, rootID := range s.rootOrder {
		if !s.completed[rootID] {
			return true
		}
	}
	return false
}

const maxGapDetailRunes = 256

func sanitizeGapDetail(detail string) string {
	if detail == "" {
		return ""
	}
	var out []rune
	for _, r := range detail {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) == maxGapDetailRunes {
			break
		}
	}
	return string(out)
}

func (s *runState) beginRoot(root Root) {
	s.mu.Lock()
	s.activeRoot = root.ID
	s.mu.Unlock()
	s.progress()
}

func (s *runState) completeRoot(root Root) {
	s.mu.Lock()
	s.completed[root.ID] = true
	s.mu.Unlock()
	s.progress()
}

func (s *runState) exclude(root Root) {
	s.mu.Lock()
	s.summaryState.ExclusionCount++
	if account := s.rootState[root.ID]; account != nil {
		account.ExclusionCount++
	}
	s.mu.Unlock()
	s.progress()
}

func (s *runState) progress() {
	if s.opts.Progress == nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if !s.lastProgress.IsZero() && now.Sub(s.lastProgress) < s.opts.ProgressInterval {
		s.mu.Unlock()
		return
	}
	s.lastProgress = now
	elapsed := now.Sub(s.started)
	p := Progress{
		Elapsed: elapsed, VisitedEntries: s.summaryState.VisitedEntries,
		VisitedDirectories: s.summaryState.VisitedDirectories,
		MatchedCount:       s.summaryState.MatchedCount, AffectingGapCount: s.summaryState.GapCount,
		ActiveRootID: s.activeRoot, CompletedRoots: len(s.completed), TotalRoots: len(s.rootOrder),
	}
	if elapsed > 0 {
		p.EntriesPerSecond = float64(p.VisitedEntries) / elapsed.Seconds()
	}
	s.mu.Unlock()
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.opts.Progress(p)
}

func (s *runState) setOutputError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.outErr == nil {
		s.outErr = err
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *runState) outputError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outErr
}

func (s *runState) setWalkError(err error) {
	s.mu.Lock()
	if s.fatal == nil {
		s.fatal = err
	}
	s.mu.Unlock()
}

func (s *runState) failWalk(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.fatal == nil {
		s.fatal = err
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *runState) walkError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fatal
}
