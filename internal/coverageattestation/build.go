package coverageattestation

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

const (
	coverageComplete = "complete"
	coveragePartial  = "partial"
)

var uuidURNPattern = regexp.MustCompile(
	`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type BuildOptions struct {
	EmitterName    string
	EmitterVersion string
	AttestationID  string
}

// Build projects reconciled inventory evidence into the canonical contract.
func Build(evidence Evidence, opts BuildOptions) (Document, error) {
	if err := validateEvidence(evidence); err != nil {
		return Document{}, err
	}
	opts, err := resolveBuildOptions(opts)
	if err != nil {
		return Document{}, err
	}

	claims := make([]Claim, 0, len(evidence.Summary.Roots)*4)
	for _, root := range evidence.Summary.Roots {
		scope := Scope{Partition: map[string]string{"root_id": root.RootID}}
		claims = append(claims,
			countClaim(scope, "entries_visited", root.VisitedEntries),
			countClaim(scope, "directories_visited", root.VisitedDirectories),
			countClaim(scope, "entries_matched", root.MatchedCount),
			countClaim(scope, "entries_emitted", root.EmittedCount),
		)
	}

	gaps, err := projectGaps(evidence.Gaps)
	if err != nil {
		return Document{}, err
	}

	state := coveragePartial
	if evidence.Summary.Lifecycle == inventory.LifecycleComplete &&
		evidence.Summary.GapCount == 0 && allRootsComplete(evidence.Summary.Roots) {
		state = coverageComplete
	}
	document := Document{
		Capabilities:  []string{Capability},
		AttestationID: opts.AttestationID,
		Subject: Subject{
			SubjectURI: "urn:spanwit:filesystem-inventory:" +
				url.PathEscape(evidence.Header.RunID),
		},
		Emitter: Emitter{
			Name: opts.EmitterName, Version: opts.EmitterVersion,
			RunID: evidence.Header.RunID, Relation: "producer",
		},
		AsOf:          evidence.Summary.TerminalObservedAt.UTC().Format(time.RFC3339Nano),
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

// resolveBuildOptions requires emitter identity and assigns or checks the
// attestation id.
func resolveBuildOptions(opts BuildOptions) (BuildOptions, error) {
	opts.EmitterName = strings.TrimSpace(opts.EmitterName)
	opts.EmitterVersion = strings.TrimSpace(opts.EmitterVersion)
	if opts.EmitterName == "" || opts.EmitterVersion == "" {
		return BuildOptions{}, errors.New(
			"coverage-attestation emitter name and version are required")
	}
	if opts.AttestationID == "" {
		var err error
		opts.AttestationID, err = newUUIDURN()
		if err != nil {
			return BuildOptions{}, err
		}
	}
	if !uuidURNPattern.MatchString(opts.AttestationID) {
		return BuildOptions{}, fmt.Errorf(
			"coverage-attestation id must be a lowercase UUID v4 URN: %q",
			opts.AttestationID)
	}
	return opts, nil
}

// projectGaps maps affecting gaps to root-scoped contract gaps with keyed
// opaque path prefixes.
func projectGaps(sources []inventory.Gap) ([]Gap, error) {
	// gapTokenKey is intentionally per-document and never serialized. A token
	// derived solely from public evidence (such as a run ID and a candidate path
	// dictionary) would reveal whether a protected path was observed.
	var gapTokenKey []byte
	if len(sources) > 0 {
		key, err := newGapTokenKey()
		if err != nil {
			return nil, err
		}
		gapTokenKey = key
	}
	gaps := make([]Gap, 0, len(sources))
	for _, source := range sources {
		scope := Scope{Partition: map[string]string{"root_id": source.RootID}}
		if source.RelativePath != "" {
			scope.Prefix = opaquePrefix(gapTokenKey, source.RootID, source.RelativePath)
		}
		gaps = append(gaps, Gap{
			Code: source.Kind, Scope: scope, Note: attestationGapNote(source.Kind),
		})
	}
	return gaps, nil
}

func validateEvidence(evidence Evidence) error {
	header := evidence.Header
	summary := evidence.Summary
	if header.RunID == "" || header.Profile != inventory.ProfileV0 {
		return errors.New("coverage-attestation requires a profile-v0 inventory header")
	}
	if header.PathProtection != inventory.PathProtectionSourceStructure {
		return errors.New("coverage-attestation requires block-export path protection")
	}
	if len(header.Roots) == 0 || len(summary.Roots) != len(header.Roots) {
		return errors.New("coverage-attestation requires one terminal row per admitted root")
	}
	if len(evidence.Gaps) > DefaultMaxGaps {
		return fmt.Errorf(
			"coverage-attestation affecting-gap limit exceeded (%d)", DefaultMaxGaps)
	}
	if header.EmissionMode != summary.EmissionMode {
		return errors.New("inventory header and summary emission modes do not match")
	}
	if header.Top != summary.Top {
		return errors.New("inventory header and summary top values do not match")
	}
	if summary.Lifecycle != inventory.LifecycleComplete &&
		summary.Lifecycle != inventory.LifecyclePartial &&
		summary.Lifecycle != inventory.LifecycleFailed {
		return fmt.Errorf("unsupported inventory lifecycle %q", summary.Lifecycle)
	}
	if summary.TerminalObservedAt.IsZero() {
		return errors.New("coverage-attestation requires an inventory terminal observation timestamp")
	}

	headerRoots := make(map[string]struct{}, len(header.Roots))
	var sums inventory.RootSummary
	for _, root := range header.Roots {
		if root.ID == "" {
			return errors.New("inventory header contains an empty root id")
		}
		if _, exists := headerRoots[root.ID]; exists {
			return fmt.Errorf("inventory header repeats root id %q", root.ID)
		}
		headerRoots[root.ID] = struct{}{}
	}
	seenSummaryRoots := make(map[string]struct{}, len(summary.Roots))
	gapsByRoot := make(map[string]int64, len(summary.Roots))
	canceledGapsByRoot := make(map[string]int64, len(summary.Roots))
	for _, gap := range evidence.Gaps {
		gapsByRoot[gap.RootID]++
		if gap.Kind == "canceled" {
			canceledGapsByRoot[gap.RootID]++
		}
	}
	var trustworthyObservations int64
	var anyRootCanceled, anyRootFailed, anyRootPartial bool
	for _, root := range summary.Roots {
		if _, exists := headerRoots[root.RootID]; !exists {
			return fmt.Errorf("terminal accounting has unknown root id %q", root.RootID)
		}
		if _, exists := seenSummaryRoots[root.RootID]; exists {
			return fmt.Errorf("terminal accounting repeats root id %q", root.RootID)
		}
		seenSummaryRoots[root.RootID] = struct{}{}
		if err := validateRootCounts(root); err != nil {
			return err
		}
		if root.GapCount != gapsByRoot[root.RootID] {
			return fmt.Errorf(
				"inventory root %s gap rows do not reconcile: rows=%d summary=%d",
				root.RootID, gapsByRoot[root.RootID], root.GapCount)
		}
		switch root.Lifecycle {
		case inventory.LifecycleComplete:
			if root.GapCount != 0 || root.Canceled || root.Failed {
				return fmt.Errorf("complete inventory root %s conflicts with gap, cancellation, or failure evidence", root.RootID)
			}
		case inventory.LifecyclePartial:
			if root.Failed {
				return fmt.Errorf("partial inventory root %s cannot be failed", root.RootID)
			}
			if root.GapCount == 0 && !root.Canceled {
				return fmt.Errorf("partial inventory root %s has no partial evidence", root.RootID)
			}
			anyRootPartial = true
		case inventory.LifecycleFailed:
			if !root.Failed {
				return fmt.Errorf("failed inventory root %s is missing failure evidence", root.RootID)
			}
			anyRootFailed = true
		}
		if root.Canceled {
			anyRootCanceled = true
			if canceledGapsByRoot[root.RootID] == 0 {
				return fmt.Errorf("canceled inventory root %s has no canceled gap", root.RootID)
			}
		}
		rootObservations :=
			root.VisitedDirectories + root.VisitedEntries + root.GapCount
		if root.Lifecycle != inventory.LifecycleComplete && rootObservations == 0 {
			return fmt.Errorf(
				"inventory root %s failed before any trustworthy scope observation; no attestation emitted",
				root.RootID)
		}
		trustworthyObservations += rootObservations
		sums.VisitedEntries += root.VisitedEntries
		sums.VisitedDirectories += root.VisitedDirectories
		sums.MatchedCount += root.MatchedCount
		sums.EmittedCount += root.EmittedCount
		sums.EmittedApparentBytes += root.EmittedApparentBytes
		sums.EmittedAllocatedBytes += root.EmittedAllocatedBytes
		sums.EmittedUnmeasuredCount += root.EmittedUnmeasuredCount
		sums.MatchedApparentBytes += root.MatchedApparentBytes
		sums.MatchedAllocatedBytes += root.MatchedAllocatedBytes
		sums.AllocatedUnmeasuredCount += root.AllocatedUnmeasuredCount
		sums.GapCount += root.GapCount
		sums.BoundarySkipCount += root.BoundarySkipCount
		sums.ExclusionCount += root.ExclusionCount
		sums.VanishedCount += root.VanishedCount
		sums.QueueLimitSkipCount += root.QueueLimitSkipCount
	}
	if sums.VisitedEntries != summary.VisitedEntries ||
		sums.VisitedDirectories != summary.VisitedDirectories ||
		sums.MatchedCount != summary.MatchedCount ||
		sums.EmittedCount != summary.EmittedCount ||
		sums.EmittedApparentBytes != summary.EmittedApparentBytes ||
		sums.EmittedAllocatedBytes != summary.EmittedAllocatedBytes ||
		sums.EmittedUnmeasuredCount != summary.EmittedUnmeasuredCount ||
		sums.MatchedApparentBytes != summary.MatchedApparentBytes ||
		sums.MatchedAllocatedBytes != summary.MatchedAllocatedBytes ||
		sums.AllocatedUnmeasuredCount != summary.AllocatedUnmeasuredCount ||
		sums.GapCount != summary.GapCount ||
		sums.BoundarySkipCount != summary.BoundarySkipCount ||
		sums.ExclusionCount != summary.ExclusionCount ||
		sums.VanishedCount != summary.VanishedCount ||
		sums.QueueLimitSkipCount != summary.QueueLimitSkipCount {
		return errors.New("inventory global totals do not reconcile to per-root rows")
	}
	if int64(len(evidence.Gaps)) != summary.GapCount {
		return fmt.Errorf(
			"inventory affecting gaps do not reconcile: rows=%d summary=%d",
			len(evidence.Gaps), summary.GapCount)
	}
	switch summary.Lifecycle {
	case inventory.LifecycleComplete:
		if summary.GapCount != 0 || summary.Canceled || anyRootPartial || anyRootFailed || !allRootsComplete(summary.Roots) {
			return errors.New("complete inventory lifecycle conflicts with root or gap evidence")
		}
	case inventory.LifecyclePartial:
		if anyRootFailed {
			return errors.New("partial inventory lifecycle cannot contain failed roots")
		}
		if !anyRootPartial && summary.GapCount == 0 && !summary.Canceled {
			return errors.New("partial inventory lifecycle has no partial evidence")
		}
		if summary.Canceled != anyRootCanceled {
			return errors.New("inventory cancellation does not reconcile to root rows")
		}
	case inventory.LifecycleFailed:
		if !anyRootFailed {
			return errors.New("failed inventory lifecycle has no failed root")
		}
	}
	if trustworthyObservations == 0 {
		return errors.New(
			"inventory failed before any trustworthy scope observation; no attestation emitted")
	}
	for _, gap := range evidence.Gaps {
		if _, exists := headerRoots[gap.RootID]; !exists {
			return fmt.Errorf("inventory gap has unknown root id %q", gap.RootID)
		}
		if !gap.AffectsCompleteness || gap.Kind == "mount-boundary" {
			return fmt.Errorf("non-affecting gap %q entered attestation evidence", gap.Kind)
		}
		if gap.Kind == "" {
			return errors.New("inventory gap kind is required")
		}
		for _, root := range header.Roots {
			if strings.Contains(gap.Detail, root.Path) {
				return errors.New("inventory gap detail contains a protected root path")
			}
		}
	}
	if summary.Top < 0 {
		return errors.New("inventory top cannot be negative")
	}
	expectedTruncated := summary.Top > 0 && summary.MatchedCount > int64(summary.Top)
	if summary.SelectionTruncated != expectedTruncated {
		return errors.New("inventory selection truncation does not reconcile to top and matched count")
	}
	switch summary.EmissionMode {
	case inventory.EmissionEntries:
		if summary.EntriesSuppressed {
			return errors.New("entry-emission inventory cannot suppress entries")
		}
		expectedEmitted := summary.MatchedCount
		if summary.Top > 0 && expectedEmitted > int64(summary.Top) {
			expectedEmitted = int64(summary.Top)
		}
		if summary.EmittedCount != expectedEmitted {
			return errors.New("inventory emitted count does not reconcile to selection")
		}
	case inventory.EmissionSummaryOnly:
		if summary.EmittedCount != 0 || summary.EmittedApparentBytes != 0 ||
			summary.EmittedAllocatedBytes != 0 || summary.EmittedUnmeasuredCount != 0 ||
			!summary.EntriesSuppressed {
			return errors.New("summary-only inventory has inconsistent emission accounting")
		}
	default:
		return fmt.Errorf("unsupported inventory emission mode %q", summary.EmissionMode)
	}
	return nil
}

func validateRootCounts(root inventory.RootSummary) error {
	counts := []int64{
		root.VisitedEntries, root.VisitedDirectories, root.MatchedCount,
		root.EmittedCount, root.EmittedApparentBytes, root.EmittedAllocatedBytes,
		root.EmittedUnmeasuredCount, root.MatchedApparentBytes, root.MatchedAllocatedBytes,
		root.AllocatedUnmeasuredCount, root.GapCount, root.BoundarySkipCount,
		root.ExclusionCount, root.VanishedCount, root.QueueLimitSkipCount,
	}
	for _, count := range counts {
		if count < 0 {
			return fmt.Errorf("inventory root %s contains a negative count", root.RootID)
		}
	}
	switch root.Lifecycle {
	case inventory.LifecycleComplete, inventory.LifecyclePartial, inventory.LifecycleFailed:
	default:
		return fmt.Errorf("inventory root %s has unsupported lifecycle %q",
			root.RootID, root.Lifecycle)
	}
	if root.Lifecycle == inventory.LifecycleComplete &&
		(root.GapCount != 0 || root.Canceled || root.Failed) {
		return fmt.Errorf("complete inventory root %s conflicts with gap/failure evidence",
			root.RootID)
	}
	return nil
}

func countClaim(scope Scope, unit string, observed int64) Claim {
	return Claim{
		Scope: scope, Basis: "confirmed", Method: "enumerated",
		Volume: &Volume{Unit: unit, Observed: observed},
	}
}

func allRootsComplete(roots []inventory.RootSummary) bool {
	for _, root := range roots {
		if root.Lifecycle != inventory.LifecycleComplete ||
			root.GapCount != 0 || root.Canceled || root.Failed {
			return false
		}
	}
	return true
}

func newGapTokenKey() ([]byte, error) {
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate coverage-attestation gap token key: %w", err)
	}
	return key, nil
}

func opaquePrefix(key []byte, rootID, relativePath string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(rootID))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(relativePath))
	return "opaque:hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func attestationGapNote(kind string) string {
	switch kind {
	case "permission":
		return "Permission prevented enumeration of the bounded scope."
	case "vanished":
		return "The bounded scope disappeared during enumeration."
	case "directory-queue-limit":
		return "The bounded directory frontier limit prevented enumeration of the scope."
	case "metadata-unavailable":
		return "Required filesystem metadata was unavailable for the bounded scope."
	case "canceled":
		return "Enumeration was canceled before the bounded scope completed."
	default:
		return "A filesystem coverage gap affected the bounded scope."
	}
}

func newUUIDURN() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate coverage-attestation id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(value[:])
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s",
		hexValue[0:8], hexValue[8:12], hexValue[12:16],
		hexValue[16:20], hexValue[20:32]), nil
}
