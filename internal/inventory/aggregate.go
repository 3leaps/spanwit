package inventory

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"sync"
)

var (
	errAggregateBudget              = errors.New("aggregate directory budget exhausted")
	errAggregateOverflow            = errors.New("aggregate accounting overflow")
	errAggregateAllocatedTopUnknown = errors.New("allocated directory top requires measured allocation for every eligible aggregate")
)

// AggregateBudgetError reports the first directory admission that did not fit
// the retained aggregate-state budget. Limit, Retained and AttemptedRetained
// are frozen together at that first rejection; AttemptedRetained is the state
// count that one admission would have produced, not the budget a complete walk
// needs (the walk stops without a census of the remaining directories). It
// carries no path. It matches errAggregateBudget under errors.Is.
type AggregateBudgetError struct {
	Limit             int64
	Retained          int64
	AttemptedRetained int64
}

func (e *AggregateBudgetError) Error() string {
	return fmt.Sprintf("%s (limit=%d retained=%d attempted_retained=%d)",
		errAggregateBudget, e.Limit, e.Retained, e.AttemptedRetained)
}

func (e *AggregateBudgetError) Is(target error) bool { return target == errAggregateBudget }

type aggregateKey struct {
	rootID string
	rel    string
}

type aggregateState struct {
	Directory
	// allocatedMeasuredSum/Count track measured allocation independently of
	// the v0 AllocatedBytesSum pointer, whose shipped v0 semantics null the
	// total when any covered file lacks allocation. The accounting projection
	// uses these counters so mixed availability yields a partial/lower claim
	// with the measured subtotal instead of discarding it.
	allocatedMeasuredSum   int64
	allocatedMeasuredCount int64
}

type aggregator struct {
	mu         sync.Mutex
	limit      int
	accounting bool
	values     map[aggregateKey]*aggregateState
	err        error
}

func newAggregator(limit int, accounting bool) *aggregator {
	return &aggregator{limit: limit, accounting: accounting, values: make(map[aggregateKey]*aggregateState)}
}

func (a *aggregator) admit(root Root, path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	rel := relative(root, path)
	key := aggregateKey{rootID: root.ID, rel: rel}
	if _, ok := a.values[key]; ok {
		return nil
	}
	if len(a.values) >= a.limit {
		retained := int64(len(a.values))
		if retained == math.MaxInt64 {
			a.err = errAggregateOverflow
			return a.err
		}
		a.err = &AggregateBudgetError{
			Limit: int64(a.limit), Retained: retained, AttemptedRetained: retained + 1,
		}
		return a.err
	}
	allocated := int64(0)
	a.values[key] = &aggregateState{Directory: Directory{
		RootID: root.ID, RelativePath: rel, Depth: relativeDepth(rel),
		Lifecycle: LifecycleComplete, AllocatedBytesSum: &allocated,
	}}
	for _, ancestor := range aggregateAncestors(rel, false) {
		state := a.values[aggregateKey{rootID: root.ID, rel: ancestor}]
		if state == nil || !checkedAdd(&state.DescendantDirectoryCount, 1) {
			a.err = errAggregateOverflow
			return a.err
		}
	}
	return nil
}

func (a *aggregator) addFile(root Root, path string, apparent int64, allocated *int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	parent := relative(root, filepath.Dir(path))
	for _, rel := range aggregateAncestors(parent, true) {
		state := a.values[aggregateKey{rootID: root.ID, rel: rel}]
		if state == nil {
			continue
		}
		if !checkedAdd(&state.FileCount, 1) ||
			!checkedAdd(&state.ApparentBytesSum, apparent) {
			a.err = errAggregateOverflow
			return a.err
		}
		if allocated == nil {
			// v0 semantics: any unmeasured file nulls the raw v0 sum.
			state.AllocatedBytesSum = nil
			if !checkedAdd(&state.AllocatedUnmeasuredCount, 1) {
				a.err = errAggregateOverflow
				return a.err
			}
		} else {
			// The v0 sum only accumulates while it has never been nulled;
			// the accounting projection keeps the full measured subtotal.
			if state.AllocatedBytesSum != nil && !checkedAdd(state.AllocatedBytesSum, *allocated) {
				a.err = errAggregateOverflow
				return a.err
			}
			if !checkedAdd(&state.allocatedMeasuredSum, *allocated) ||
				!checkedAdd(&state.allocatedMeasuredCount, 1) {
				a.err = errAggregateOverflow
				return a.err
			}
		}
	}
	return nil
}

func (a *aggregator) addGap(root Root, path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	rel := relative(root, path)
	if rel == "" {
		rel = "."
	}
	// A directory gap affects that directory if admitted; a file/unknown gap
	// starts at its containing directory. Including both safely marks all
	// admitted ancestors without inventing an aggregate for an unreadable child.
	for _, ancestor := range aggregateAncestors(rel, true) {
		state := a.values[aggregateKey{rootID: root.ID, rel: ancestor}]
		if state == nil {
			continue
		}
		state.Lifecycle = LifecyclePartial
		if !checkedAdd(&state.AffectingGapCount, 1) {
			a.err = errAggregateOverflow
			return a.err
		}
	}
	return nil
}

func (a *aggregator) records(depth, top int, basis string) ([]Directory, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return nil, a.err
	}
	out := make([]Directory, 0, len(a.values))
	for _, state := range a.values {
		if depth >= 0 && state.Depth > depth {
			continue
		}
		record := state.Directory
		if a.accounting {
			record.Accounting = newDirectoryAccounting(state)
		}
		out = append(out, record)
	}
	if basis == SizeAllocated && top > 0 {
		for _, directory := range out {
			if directory.AllocatedBytesSum == nil {
				return nil, errAggregateAllocatedTopUnknown
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].ApparentBytesSum, out[j].ApparentBytesSum
		if basis == SizeAllocated {
			left, right = directoryAllocatedRank(out[i]), directoryAllocatedRank(out[j])
		}
		if left != right {
			return left > right
		}
		if out[i].RootID != out[j].RootID {
			return out[i].RootID < out[j].RootID
		}
		return out[i].RelativePath < out[j].RelativePath
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out, nil
}

func directoryAllocatedRank(value Directory) int64 {
	if value.AllocatedBytesSum == nil {
		return 0
	}
	return *value.AllocatedBytesSum
}

// newDirectoryAccounting qualifies one directory aggregate's byte planes. The
// bound is exact only for a complete declared subject; affecting coverage gaps
// downgrade observed sums to partial/lower. A partially measured allocation sum
// is an observed partial/lower claim carrying the measured subtotal and the
// unmeasured count; unsupported is reserved for zero measured entries and
// never substitutes apparent size for allocation.
func newDirectoryAccounting(state *aggregateState) *DirectoryAccounting {
	dir := state.Directory
	lowerBound := dir.Lifecycle != LifecycleComplete
	apparent := AccountingClaim{
		Status: ClaimStatusMeasured,
		Basis:  BasisApparentPathEntrySum,
		Bound:  BoundExact,
		Bytes:  int64Pointer(dir.ApparentBytesSum),
	}
	if lowerBound {
		apparent.Status = ClaimStatusPartial
		apparent.Bound = BoundLower
	}
	allocated := AccountingClaim{}
	fullyMeasured := state.allocatedMeasuredCount == dir.FileCount
	switch {
	case fullyMeasured:
		allocated.Basis = BasisAllocatedPathEntrySum
		allocated.Status = ClaimStatusMeasured
		allocated.Bound = BoundExact
		sum := state.allocatedMeasuredSum
		allocated.Bytes = &sum
		if lowerBound {
			allocated.Status = ClaimStatusPartial
			allocated.Bound = BoundLower
		}
	case state.allocatedMeasuredCount == 0:
		allocated.Status = ClaimStatusUnsupported
		allocated.Detail = "no allocation observation was available for any covered file"
	default:
		allocated.Basis = BasisAllocatedPathEntrySum
		allocated.Status = ClaimStatusPartial
		allocated.Bound = BoundLower
		sum := state.allocatedMeasuredSum
		allocated.Bytes = &sum
		allocated.UnmeasuredCount = int64Pointer(dir.AllocatedUnmeasuredCount)
	}
	return &DirectoryAccounting{
		Apparent:  apparent,
		Allocated: allocated,
		UniquePhysical: AccountingClaim{
			Status: ClaimStatusUnsupported,
			Detail: "unique physical extent consumption is not observable through portable public file metadata",
		},
		SharedCloned: AccountingClaim{
			Status: ClaimStatusUnsupported,
			Detail: "shared or cloned extent ownership is not observable through portable public file metadata",
		},
		ExpectedReclaim: AccountingClaim{
			Status: ClaimStatusUnsupported,
			Detail: "expected post-deletion reclaim is not derivable from observed file sums",
		},
	}
}

func int64Pointer(value int64) *int64 {
	out := value
	return &out
}

func (a *aggregator) eligibleCount(depth int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, state := range a.values {
		if depth < 0 || state.Depth <= depth {
			count++
		}
	}
	return count
}

func checkedAdd(dst *int64, value int64) bool {
	if value < 0 || *dst > math.MaxInt64-value {
		return false
	}
	*dst += value
	return true
}

func aggregateAncestors(rel string, includeSelf bool) []string {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if rel == "" || rel == "." {
		if !includeSelf {
			return nil
		}
		return []string{"."}
	}
	if !includeSelf {
		rel = filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	}
	out := make([]string, 0, relativeDepth(rel)+1)
	for {
		out = append(out, rel)
		if rel == "." {
			break
		}
		rel = filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	}
	return out
}

func relativeDepth(rel string) int {
	if rel == "" || rel == "." {
		return 0
	}
	depth := 1
	for _, r := range rel {
		if r == '/' {
			depth++
		}
	}
	return depth
}
