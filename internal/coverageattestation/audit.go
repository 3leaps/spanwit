package coverageattestation

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

// Directory-audit selection units. They count directory aggregates, never
// file matches or emissions, and are scoped to the run because selection is
// global across roots.
const (
	unitDirectoriesSelectedBeforeTop = "directories_selected_before_top"
	unitDirectoriesEmitted           = "directories_emitted"
)

// auditDownstream is the ordinary output sink for a directory-audit run.
type auditDownstream interface {
	inventory.Sink
	inventory.AuditSink
}

// WithheldError reports a requested directory-audit attestation that was not
// written because the audit result cannot support one. No file is created.
type WithheldError struct {
	Reason string
}

func (e *WithheldError) Error() string {
	return "coverage attestation withheld: " + e.Reason + "; no attestation file was written"
}

// AuditEvidence is the reconciled input for a directory-audit attestation.
// Only AuditCollector constructs it: every record was accepted by the
// downstream sink and checked by the stream semantics before it counted.
// Its fields are unexported so checked evidence cannot be edited before it is
// projected.
type AuditEvidence struct {
	header  inventory.AuditHeader
	gaps    []inventory.Gap
	summary inventory.AuditSummary

	admittedRoots []admittedRootIdentity
	reconciled    bool
}

// TerminalObservedAt is the audit's terminal observation time, used as the
// attestation as_of.
func (e AuditEvidence) TerminalObservedAt() time.Time { return e.summary.TerminalObservedAt }

// AuditCollector tees a directory-audit stream to the ordinary sink and keeps
// only the header, affecting gaps and terminal summary. Directory rows are
// counted and checked online as they are accepted downstream, never retained.
type AuditCollector struct {
	downstream auditDownstream
	maxGaps    int

	mu       sync.Mutex
	checker  *inventory.AuditStreamChecker
	header   *inventory.AuditHeader
	roots    []admittedRootIdentity
	gaps     []inventory.Gap
	summary  *inventory.AuditSummary
	overflow bool
	// invalid records the first stream-semantics violation; the output is
	// still forwarded unchanged, but no attestation may be built from it.
	invalid error
}

// NewAuditCollector wraps the directory-audit output sink.
func NewAuditCollector(downstream inventory.Sink, maxGaps int) (*AuditCollector, error) {
	sink, ok := downstream.(auditDownstream)
	if !ok || downstream == nil {
		return nil, errors.New("coverage-attestation audit collector requires a directory-audit sink")
	}
	if maxGaps < 0 {
		return nil, errors.New("coverage-attestation gap limit cannot be negative")
	}
	if maxGaps == 0 {
		maxGaps = DefaultMaxGaps
	}
	return &AuditCollector{
		downstream: sink, maxGaps: maxGaps, checker: inventory.NewAuditStreamChecker(),
	}, nil
}

// Header, Entry and Summary belong to the legacy profiles; a directory-audit
// run never emits them, so receiving one fails closed.
func (c *AuditCollector) Header(inventory.Header) error {
	return errors.New("coverage-attestation audit collector received a legacy header")
}

func (c *AuditCollector) Entry(inventory.Entry) error {
	return errors.New("coverage-attestation audit collector received a file entry")
}

func (c *AuditCollector) Summary(inventory.Summary) error {
	return errors.New("coverage-attestation audit collector received a legacy summary")
}

func (c *AuditCollector) AuditHeader(header inventory.AuditHeader) error {
	identities, err := captureAdmittedRoots(header.Roots)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.downstream.AuditHeader(header); err != nil {
		return err
	}
	retained := header
	retained.Roots = append([]inventory.Root(nil), header.Roots...)
	retained.Exclusions = append([]inventory.Exclusion(nil), header.Exclusions...)
	c.header = &retained
	c.roots = identities
	c.check(c.checker.Header(retained))
	return nil
}

func (c *AuditCollector) Gap(gap inventory.Gap) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.downstream.Gap(gap); err != nil {
		return err
	}
	c.check(c.checker.Gap(gap))
	if gap.AffectsCompleteness {
		if len(c.gaps) < c.maxGaps {
			c.gaps = append(c.gaps, gap)
		} else {
			c.overflow = true
		}
	}
	return nil
}

func (c *AuditCollector) AuditDirectory(row inventory.AuditDirectory) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.downstream.AuditDirectory(row); err != nil {
		return err
	}
	c.check(c.checker.Directory(row))
	return nil
}

func (c *AuditCollector) AuditSummary(summary inventory.AuditSummary) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.downstream.AuditSummary(summary); err != nil {
		return err
	}
	retained := summary
	retained.Roots = append([]inventory.AuditRootSummary(nil), summary.Roots...)
	if summary.AuditSelectionCounts != nil {
		counts := *summary.AuditSelectionCounts
		retained.AuditSelectionCounts = &counts
	}
	if summary.Failure != nil {
		failure := *summary.Failure
		retained.Failure = &failure
	}
	c.summary = &retained
	c.check(c.checker.Summary(retained))
	return nil
}

func (c *AuditCollector) check(err error) {
	if err != nil && c.invalid == nil {
		c.invalid = err
	}
}

// Evidence returns reconciled evidence, or a *WithheldError when the audit
// result does not support an attestation.
func (c *AuditCollector) Evidence() (AuditEvidence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.header == nil {
		return AuditEvidence{}, &WithheldError{Reason: "the audit stream has no header"}
	}
	if c.summary == nil {
		return AuditEvidence{}, &WithheldError{
			Reason: "the audit did not write its terminal summary"}
	}
	// The audit's own result decides withholding first, so requesting an
	// attestation never changes the exit status of an unpublishable audit.
	if err := withholdReason(*c.summary); err != nil {
		return AuditEvidence{}, err
	}
	if c.invalid != nil {
		// The checker's message can name directories; keep it off stderr.
		return AuditEvidence{}, errors.New(
			"coverage-attestation audit evidence does not reconcile with the audit stream; no attestation emitted")
	}
	if c.overflow {
		return AuditEvidence{}, fmt.Errorf(
			"coverage-attestation affecting-gap limit exceeded (%d); no attestation emitted",
			c.maxGaps)
	}
	summary := *c.summary
	summary.Roots = append([]inventory.AuditRootSummary(nil), c.summary.Roots...)
	if c.summary.AuditSelectionCounts != nil {
		counts := *c.summary.AuditSelectionCounts
		summary.AuditSelectionCounts = &counts
	}
	header := *c.header
	header.Roots = append([]inventory.Root(nil), c.header.Roots...)
	return AuditEvidence{
		header: header, gaps: append([]inventory.Gap(nil), c.gaps...),
		summary: summary, admittedRoots: append([]admittedRootIdentity(nil), c.roots...),
		reconciled: true,
	}, nil
}

func (e AuditEvidence) admittedRootPaths() ([]string, error) {
	return admittedRootPaths(e.header.Roots, e.admittedRoots)
}

func (e AuditEvidence) verifyAdmittedRootIdentities() error {
	return verifyAdmittedRootIdentities(e.header.Roots, e.admittedRoots)
}

// BuildAudit projects directory-audit evidence into the canonical contract.
// It attests the full declared traversal subject: floor, depth and top are
// output selection, not coverage gaps.
func BuildAudit(evidence AuditEvidence, opts BuildOptions) (Document, error) {
	if err := validateAuditEvidence(evidence); err != nil {
		return Document{}, err
	}
	opts, err := resolveBuildOptions(opts)
	if err != nil {
		return Document{}, err
	}
	header, summary := evidence.header, evidence.summary

	claims := make([]Claim, 0, len(summary.Roots)*2+2)
	for _, root := range summary.Roots {
		scope := Scope{Partition: map[string]string{"root_id": root.RootID}}
		claims = append(claims,
			countClaim(scope, "entries_visited", root.VisitedEntries),
			countClaim(scope, "directories_visited", root.VisitedDirectories),
		)
	}
	run := Scope{Partition: map[string]string{"run_id": header.RunID}}
	claims = append(claims,
		Claim{
			Scope: run, Basis: "confirmed", Method: "derived",
			Volume: &Volume{Unit: unitDirectoriesSelectedBeforeTop,
				Observed: summary.SelectedBeforeTop},
		},
		countClaim(run, unitDirectoriesEmitted, summary.DirectoryEmittedCount),
	)
	gaps, err := projectGaps(evidence.gaps)
	if err != nil {
		return Document{}, err
	}

	state := coveragePartial
	if summary.Lifecycle == inventory.LifecycleComplete && summary.GapCount == 0 {
		state = coverageComplete
	}
	document := Document{
		Capabilities:  []string{Capability},
		AttestationID: opts.AttestationID,
		Subject: Subject{
			SubjectURI: "urn:spanwit:filesystem-inventory:" + url.PathEscape(header.RunID),
		},
		Emitter: Emitter{
			Name: opts.EmitterName, Version: opts.EmitterVersion,
			RunID: header.RunID, Relation: "producer",
		},
		AsOf:          summary.TerminalObservedAt.UTC().Format(time.RFC3339Nano),
		CoverageState: state,
		Claims:        claims,
		Gaps:          gaps,
		Protection:    Protection{DefaultAction: "block_export"},
	}
	if _, err := MarshalValidated(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

// validateAuditEvidence applies the publication gates. The stream semantics
// (counts, roots, gap reconciliation, lifecycle relations) were checked by
// the collector; this decides whether the result may be attested at all.
func validateAuditEvidence(evidence AuditEvidence) error {
	if !evidence.reconciled {
		return errors.New("coverage-attestation audit evidence was not collected from a checked stream")
	}
	header, summary := evidence.header, evidence.summary
	if header.RunID == "" || header.Profile != inventory.AggregationProfileV2 ||
		header.EmissionMode != inventory.EmissionDirectoryAudit {
		return errors.New("coverage-attestation requires a directory-audit v2 header")
	}
	if header.PathProtection != inventory.PathProtectionSourceStructure {
		return errors.New("coverage-attestation requires block-export path protection")
	}
	if err := withholdReason(summary); err != nil {
		return err
	}
	if summary.AuditSelectionCounts == nil {
		return errors.New("coverage-attestation requires reconciled selection counts")
	}
	if summary.Lifecycle != inventory.LifecycleComplete && summary.Lifecycle != inventory.LifecyclePartial {
		return fmt.Errorf("unsupported audit lifecycle %q", summary.Lifecycle)
	}
	if summary.TerminalObservedAt.IsZero() {
		return errors.New("coverage-attestation requires an audit terminal observation timestamp")
	}
	if len(header.Roots) == 0 || len(summary.Roots) != len(header.Roots) {
		return errors.New("coverage-attestation requires one terminal row per admitted root")
	}
	if int64(len(evidence.gaps)) != summary.GapCount {
		return fmt.Errorf("audit affecting gaps do not reconcile: rows=%d summary=%d",
			len(evidence.gaps), summary.GapCount)
	}
	for _, root := range summary.Roots {
		if root.Failed || root.Canceled {
			return fmt.Errorf("audit root %s is failed or canceled under a publishable result", root.RootID)
		}
		if root.Lifecycle != inventory.LifecycleComplete &&
			root.VisitedDirectories+root.VisitedEntries+root.GapCount == 0 {
			return fmt.Errorf(
				"audit root %s has no trustworthy scope observation; no attestation emitted", root.RootID)
		}
	}
	for _, gap := range evidence.gaps {
		if !gap.AffectsCompleteness || gap.Kind == "" || gap.Kind == "mount-boundary" {
			return fmt.Errorf("non-affecting gap %q entered attestation evidence", gap.Kind)
		}
		for _, root := range header.Roots {
			if strings.Contains(gap.Detail, root.Path) {
				return errors.New("audit gap detail contains a protected root path")
			}
		}
	}
	return nil
}

// withholdReason reports an audit result that cannot support an attestation.
func withholdReason(summary inventory.AuditSummary) error {
	switch {
	case summary.Lifecycle == inventory.LifecycleFailed:
		return &WithheldError{Reason: "the audit failed"}
	case summary.Canceled:
		return &WithheldError{Reason: "the audit was canceled"}
	case !summary.SelectionReconciled:
		return &WithheldError{Reason: "the directory selection did not reconcile"}
	case !summary.EmissionCompleted:
		return &WithheldError{Reason: "the audit output did not complete"}
	}
	return nil
}
