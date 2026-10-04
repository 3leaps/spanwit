package bench

import (
	"testing"
	"time"
)

// rowWith builds a measurement for aggregation tests.
func rowWith(t *testing.T, tool string, workers int, cache CacheState, wall time.Duration,
	examined int64, purgeOK bool,
) *Measurement {
	t.Helper()
	env := completeEnv()
	env.Workers = workers
	env.CacheState = cache
	if cache == CacheCold {
		env.CachePurgeAttempted = true
		env.CachePurgeSucceeded = purgeOK
		if !purgeOK {
			env.CachePurgeDetail = "Operation not permitted"
		}
	}
	res := Result{
		WallTime:        wall,
		EntriesExamined: examined,
		Outcome:         OutcomeComplete,
		PeakOpenFDs:     -1,
	}
	m, err := NewMeasurement(tool, "dev", 0, env, res, CountEntriesVisited, WorkMetadata)
	if err != nil {
		t.Fatalf("build row: %v", err)
	}
	return m
}

// TestAggregateUsesMedianNotMean confirms a single slow run does not move the
// headline number, which is the reason for choosing the median.
func TestAggregateUsesMedianNotMean(t *testing.T) {
	var set Set
	for _, wall := range []time.Duration{
		100 * time.Millisecond,
		102 * time.Millisecond,
		101 * time.Millisecond,
		103 * time.Millisecond,
		5 * time.Second, // one scheduler hiccup
	} {
		set.Add(rowWith(t, "parallel-bounded", 4, CacheWarm, wall, 1000, false))
	}

	aggs := set.Aggregated()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 group, got %d", len(aggs))
	}
	agg := aggs[0]

	if agg.WallMedian != 102*time.Millisecond {
		t.Errorf("median %v, want 102ms", agg.WallMedian)
	}
	if agg.WallMax != 5*time.Second {
		t.Errorf("max %v, want 5s", agg.WallMax)
	}
	// The spread must expose the outlier rather than hide it.
	if agg.Spread() < 40 {
		t.Errorf("spread %v does not reflect a 50x outlier", agg.Spread())
	}
}

// TestAggregateRefusesTrustWhenCountsDisagree covers the check that stops a
// timing comparison across runs that did different amounts of work.
func TestAggregateRefusesTrustWhenCountsDisagree(t *testing.T) {
	var set Set
	set.Add(rowWith(t, "find", 1, CacheWarm, 200*time.Millisecond, 1000, false))
	set.Add(rowWith(t, "find", 1, CacheWarm, 205*time.Millisecond, 1000, false))
	set.Add(rowWith(t, "find", 1, CacheWarm, 198*time.Millisecond, 980, false))

	agg := set.Aggregated()[0]
	if agg.CountsAgree {
		t.Fatal("group with differing entry counts reported agreement")
	}
	if agg.EntriesPerSecond() != 0 {
		t.Errorf("a disagreeing group produced a rate of %v", agg.EntriesPerSecond())
	}
	ok, reasons := agg.Trustworthy()
	if ok {
		t.Fatal("group with differing counts was reported trustworthy")
	}
	if len(reasons) == 0 {
		t.Error("no reason given for untrustworthiness")
	}
}

// TestAggregateMarksUnenforcedCold is the check that keeps an unenforced cold
// label from being read as cold-cache evidence.
func TestAggregateMarksUnenforcedCold(t *testing.T) {
	var enforced, unenforced Set
	for i := 0; i < 3; i++ {
		enforced.Add(rowWith(t, "parallel-bounded", 4, CacheCold, 200*time.Millisecond, 1000, true))
		unenforced.Add(rowWith(t, "parallel-bounded", 4, CacheCold, 200*time.Millisecond, 1000, false))
	}

	if agg := enforced.Aggregated()[0]; agg.ColdUnenforced {
		t.Error("an enforced cold group was marked unenforced")
	} else if ok, reasons := agg.Trustworthy(); !ok {
		t.Errorf("enforced cold group not trustworthy: %v", reasons)
	}

	agg := unenforced.Aggregated()[0]
	if !agg.ColdUnenforced {
		t.Fatal("an unenforced cold group was not marked")
	}
	if agg.PurgeDetail == "" {
		t.Error("unenforced cold group does not say why")
	}
	ok, reasons := agg.Trustworthy()
	if ok {
		t.Fatal("unenforced cold group reported trustworthy")
	}
	var found bool
	for _, r := range reasons {
		if r == "cold cache claimed but not enforced" {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons do not name the unenforced cold cache: %v", reasons)
	}
}

// TestAggregateSeparatesGroups confirms rows differing in a controlled
// dimension are not silently pooled.
func TestAggregateSeparatesGroups(t *testing.T) {
	var set Set
	for _, workers := range []int{1, 4} {
		for i := 0; i < 3; i++ {
			set.Add(rowWith(t, "parallel-bounded", workers, CacheWarm, 200*time.Millisecond, 1000, false))
		}
	}
	set.Add(rowWith(t, "walkdir-serial", 1, CacheWarm, 300*time.Millisecond, 1000, false))

	aggs := set.Aggregated()
	if len(aggs) != 3 {
		t.Fatalf("expected 3 groups (2 worker counts + 1 tool), got %d", len(aggs))
	}
	for _, agg := range aggs {
		if agg.Group.Tool == "walkdir-serial" && agg.Repetitions != 1 {
			t.Errorf("serial group has %d repetitions, want 1", agg.Repetitions)
		}
	}
}

// TestAggregateFlagsTooFewRepetitions confirms a single run is not presented
// as a measurement a comparison can rest on.
func TestAggregateFlagsTooFewRepetitions(t *testing.T) {
	var set Set
	set.Add(rowWith(t, "find", 1, CacheWarm, 500*time.Millisecond, 1000, false))

	ok, reasons := set.Aggregated()[0].Trustworthy()
	if ok {
		t.Fatal("a single repetition was reported trustworthy")
	}
	if len(reasons) == 0 || reasons[0] == "" {
		t.Error("no reason given")
	}
}
