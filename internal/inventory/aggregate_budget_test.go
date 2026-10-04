package inventory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireBudgetError(t *testing.T, err error, limit, retained, attempted int64) {
	t.Helper()
	if !errors.Is(err, errAggregateBudget) {
		t.Fatalf("error=%v does not match errAggregateBudget", err)
	}
	var budgetErr *AggregateBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error=%T is not *AggregateBudgetError", err)
	}
	if budgetErr.Limit != limit || budgetErr.Retained != retained || budgetErr.AttemptedRetained != attempted {
		t.Fatalf("budget=%+v want limit=%d retained=%d attempted=%d", budgetErr, limit, retained, attempted)
	}
	if budgetErr.Retained != budgetErr.Limit || budgetErr.AttemptedRetained != budgetErr.Retained+1 {
		t.Fatalf("budget invariants violated: %+v", budgetErr)
	}
}

func TestAggregateBudgetExactFitAndFirstRejection(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	a := newAggregator(2, false)
	if err := a.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	if err := a.admit(root, filepath.Join(root.Path, "a")); err != nil {
		t.Fatalf("exact fit rejected: %v", err)
	}
	// Re-admitting a retained directory consumes no budget and is not a rejection.
	if err := a.admit(root, filepath.Join(root.Path, "a")); err != nil {
		t.Fatalf("duplicate admission at exact fit rejected: %v", err)
	}
	first := a.admit(root, filepath.Join(root.Path, "b"))
	requireBudgetError(t, first, 2, 2, 3)
	// Later attempts return the same frozen first failure, not a new count.
	later := a.admit(root, filepath.Join(root.Path, "c"))
	if later != first {
		t.Fatalf("later error=%v is not the frozen first failure %v", later, first)
	}
	if got := len(a.values); got != 2 {
		t.Fatalf("retained=%d after rejection, want 2", got)
	}
}

func TestAggregateBudgetConcurrentExhaustionFreezesOneFailure(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	const limit = 8
	a := newAggregator(limit, false)
	// The walker admits a parent before its children; mirror that ordering.
	if err := a.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 64)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = a.admit(root, filepath.Join(root.Path, fmt.Sprintf("d%02d", i)))
		}(i)
	}
	wg.Wait()
	var first error
	admitted := 0
	for _, err := range errs {
		if err == nil {
			admitted++
			continue
		}
		if first == nil {
			first = err
		} else if err != first {
			t.Fatalf("distinct failures %v and %v; first failure must be frozen", first, err)
		}
	}
	if admitted != limit-1 || len(a.values) != limit {
		t.Fatalf("admitted=%d retained=%d want %d children, %d states", admitted, len(a.values), limit-1, limit)
	}
	requireBudgetError(t, first, limit, limit, limit+1)
}

func TestAggregateBudgetErrorIsPathFree(t *testing.T) {
	root := t.TempDir()
	const secret = "zz-rejected-secret-dir"
	writeSizedAt(t, filepath.Join(root, "a", "x.bin"), 1, time.Now())
	writeSizedAt(t, filepath.Join(root, secret, "y.bin"), 1, time.Now())
	_, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 2,
	}, &memorySink{})
	requireBudgetError(t, err, 2, 2, 3)
	wrapped := fmt.Errorf("inventory: %w", err)
	for _, text := range []string{err.Error(), wrapped.Error()} {
		if strings.Contains(text, secret) || strings.Contains(text, root) {
			t.Fatalf("budget error discloses a path: %q", text)
		}
	}
}

func TestAggregateBudgetShallowDepthStillFailsAndRaisedBudgetSucceeds(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/deep/x.bin", "b/deep/y.bin", "c/z.bin"} {
		writeSizedAt(t, filepath.Join(root, filepath.FromSlash(rel)), 3, time.Now())
	}
	opts := Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: 0,
		MaxAggregateDirectories: 3,
	}
	sink := &memorySink{}
	summary, err := Run(context.Background(), opts, sink)
	requireBudgetError(t, err, 3, 3, 4)
	if summary.Lifecycle != LifecycleFailed || len(sink.directories) != 0 {
		t.Fatalf("summary=%+v directories=%+v", summary, sink.directories)
	}

	var baseline []Directory
	for _, budget := range []int{6, 1000} {
		opts.MaxAggregateDirectories = budget
		raised := &memorySink{}
		summary, err := Run(context.Background(), opts, raised)
		if err != nil || summary.Lifecycle != LifecycleComplete {
			t.Fatalf("budget=%d err=%v summary=%+v", budget, err, summary)
		}
		if len(raised.directories) != 1 || raised.directories[0].ApparentBytesSum != 9 {
			t.Fatalf("budget=%d directories=%+v", budget, raised.directories)
		}
		if baseline == nil {
			baseline = raised.directories
		} else if !reflect.DeepEqual(baseline, raised.directories) {
			t.Fatalf("totals changed with budget: %+v vs %+v", baseline, raised.directories)
		}
	}
}
