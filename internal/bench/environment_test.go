package bench

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// completeEnv returns an environment that passes validation, for tests that
// need to vary exactly one field away from valid.
func completeEnv() Environment {
	return Environment{
		OS:                   "darwin",
		Arch:                 "arm64",
		KernelVersion:        "Darwin 25.5.0",
		GoVersion:            "go1.26.4",
		CPULogical:           10,
		FilesystemType:       "apfs",
		FilesystemTypeSource: SourceMeasured,
		DeviceClass:          DeviceSSD,
		DeviceClassSource:    SourceDeclared,
		CacheState:           CacheWarm,
		CacheStateSource:     SourceDeclared,
		Workers:              4,
		CorpusID:             "wide/scale=1000/seed=0/size=1024",
		CorpusCoverage:       "all requested shape elements present",
	}
}

func okResult() Result {
	return Result{
		WallTime:        2 * time.Second,
		EntriesExamined: 1000,
		Outcome:         OutcomeComplete,
		PeakOpenFDs:     -1,
	}
}

// TestMeasurementRefusesIncompleteEnvironment is the mechanism test. Each case
// removes exactly one required field; none may produce a row.
func TestMeasurementRefusesIncompleteEnvironment(t *testing.T) {
	cases := []struct {
		field  string
		mutate func(*Environment)
	}{
		{"os", func(e *Environment) { e.OS = "" }},
		{"arch", func(e *Environment) { e.Arch = "" }},
		{"kernel_version", func(e *Environment) { e.KernelVersion = "" }},
		{"go_version", func(e *Environment) { e.GoVersion = "" }},
		{"cpu_logical", func(e *Environment) { e.CPULogical = 0 }},
		{"filesystem_type", func(e *Environment) { e.FilesystemType = "" }},
		{"filesystem_type_source", func(e *Environment) { e.FilesystemTypeSource = "" }},
		{"device_class", func(e *Environment) { e.DeviceClass = "" }},
		{"device_class_source", func(e *Environment) { e.DeviceClassSource = "" }},
		{"cache_state", func(e *Environment) { e.CacheState = "" }},
		{"cache_state_source", func(e *Environment) { e.CacheStateSource = "" }},
		{"workers", func(e *Environment) { e.Workers = 0 }},
		{"corpus_id", func(e *Environment) { e.CorpusID = "" }},
		{"corpus_coverage", func(e *Environment) { e.CorpusCoverage = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			env := completeEnv()
			tc.mutate(&env)

			if err := env.Validate(); err == nil {
				t.Fatalf("validation accepted an environment missing %s", tc.field)
			} else if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("validation error does not name %s: %v", tc.field, err)
			}

			m, err := NewMeasurement("walkdir-serial", "go1.26.4", 0, env, okResult(), CountEntriesVisited, WorkMetadata)
			if err == nil {
				t.Fatalf("NewMeasurement produced a row missing %s", tc.field)
			}
			if m != nil {
				t.Error("NewMeasurement returned a row alongside an error")
			}
			if !errors.Is(err, ErrIncompleteEnvironment) {
				t.Errorf("error does not wrap ErrIncompleteEnvironment: %v", err)
			}
		})
	}
}

// TestZeroEnvironmentNamesEveryMissingField confirms an operator gets the whole
// list at once rather than one field per run.
func TestZeroEnvironmentNamesEveryMissingField(t *testing.T) {
	err := Environment{}.Validate()
	if err == nil {
		t.Fatal("empty environment validated")
	}
	for _, field := range []string{
		"os", "arch", "kernel_version", "go_version", "cpu_logical",
		"filesystem_type", "device_class", "cache_state", "workers",
		"corpus_id", "corpus_coverage",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error does not name missing field %q: %v", field, err)
		}
	}
}

// TestUnknownDeviceClassIsEmittable draws the line between a stated unknown and
// an absent field. An operator who does not know the device class may say so;
// one who says nothing may not emit.
func TestUnknownDeviceClassIsEmittable(t *testing.T) {
	env := completeEnv()
	env.DeviceClass = DeviceUnknown
	if err := env.Validate(); err != nil {
		t.Fatalf("a stated unknown device class must be emittable: %v", err)
	}

	env.DeviceClass = ""
	if err := env.Validate(); err == nil {
		t.Fatal("an absent device class must not be emittable")
	}
}

// TestMeasurementRequiresToolIdentity covers the non-environment refusals.
func TestMeasurementRequiresToolIdentity(t *testing.T) {
	env := completeEnv()

	if _, err := NewMeasurement("", "1.0", 0, env, okResult(), CountEntriesVisited, WorkMetadata); err == nil {
		t.Error("accepted a measurement with no tool name")
	}
	if _, err := NewMeasurement("find", "", 0, env, okResult(), CountEntriesVisited, WorkMetadata); err == nil {
		t.Error("accepted a measurement with no tool version")
	}
	if _, err := NewMeasurement("find", "1.0", -1, env, okResult(), CountEntriesVisited, WorkMetadata); err == nil {
		t.Error("accepted a measurement with a negative repetition")
	}

	noOutcome := okResult()
	noOutcome.Outcome = ""
	if _, err := NewMeasurement("find", "1.0", 0, env, noOutcome, CountEntriesVisited, WorkMetadata); err == nil {
		t.Error("accepted a measurement with no outcome")
	}
}

// TestValidMeasurementCarriesEnvironmentInTheRow confirms the environment
// travels with the number through serialization, which is the whole point of
// binding them.
func TestValidMeasurementCarriesEnvironmentInTheRow(t *testing.T) {
	m, err := NewMeasurement("walkdir-serial", "go1.26.4", 0, completeEnv(), okResult(), CountEntriesVisited, WorkMetadata)
	if err != nil {
		t.Fatalf("valid measurement rejected: %v", err)
	}

	var set Set
	set.Add(m)
	var buf bytes.Buffer
	if err := set.WriteJSONL(&buf); err != nil {
		t.Fatalf("write: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	env, ok := decoded["environment"].(map[string]any)
	if !ok {
		t.Fatal("serialized row carries no environment")
	}
	for _, field := range []string{
		"filesystem_type", "device_class", "cache_state", "workers",
		"corpus_id", "corpus_coverage", "kernel_version",
	} {
		if v, present := env[field]; !present || v == "" {
			t.Errorf("serialized environment is missing %q", field)
		}
	}

	if got := decoded["derived"].(map[string]any)["entries_per_second"].(float64); got != 500 {
		t.Errorf("entries per second: got %v, want 500", got)
	}
}

// TestComparableDetectsUncontrolledComparison covers the check that stops two
// rows from different conditions being presented side by side.
func TestComparableDetectsUncontrolledComparison(t *testing.T) {
	a := completeEnv()

	sweptWorkers := a
	sweptWorkers.Workers = 8
	if ok, _ := a.Comparable(sweptWorkers, "workers"); !ok {
		t.Error("a worker sweep should be comparable when workers is the swept dimension")
	}
	if ok, differing := a.Comparable(sweptWorkers); ok {
		t.Error("differing workers should not be comparable when nothing is swept")
	} else if len(differing) != 1 || differing[0] != "workers" {
		t.Errorf("expected workers to be named as differing, got %v", differing)
	}

	differentFS := a
	differentFS.FilesystemType = "ext4"
	differentFS.Workers = 8
	ok, differing := a.Comparable(differentFS, "workers")
	if ok {
		t.Fatal("rows from different filesystems must not compare as controlled")
	}
	if len(differing) != 1 || differing[0] != "filesystem_type" {
		t.Errorf("expected filesystem_type to be named, got %v", differing)
	}
}

// TestSetCheckComparable exercises the same guard at the set level, which is
// where a results table is actually built.
func TestSetCheckComparable(t *testing.T) {
	var set Set
	for _, workers := range []int{1, 2, 4} {
		env := completeEnv()
		env.Workers = workers
		m, err := NewMeasurement("parallel-walk", "dev", 0, env, okResult(), CountEntriesVisited, WorkMetadata)
		if err != nil {
			t.Fatalf("build row: %v", err)
		}
		set.Add(m)
	}
	if err := set.CheckComparable("workers"); err != nil {
		t.Errorf("worker sweep flagged as uncontrolled: %v", err)
	}
	if err := set.CheckComparable(); err == nil {
		t.Error("a set varying workers passed a check that swept nothing")
	}

	// Slip one row from a different corpus into the sweep.
	env := completeEnv()
	env.Workers = 8
	env.CorpusID = "deep/scale=100/seed=0/size=1024"
	m, err := NewMeasurement("parallel-walk", "dev", 0, env, okResult(), CountEntriesVisited, WorkMetadata)
	if err != nil {
		t.Fatalf("build row: %v", err)
	}
	set.Add(m)

	err = set.CheckComparable("workers")
	if err == nil {
		t.Fatal("a set spanning two corpora passed a controlled-comparison check")
	}
	var cmpErr *ComparisonError
	if !errors.As(err, &cmpErr) {
		t.Fatalf("expected a ComparisonError, got %T", err)
	}
	if len(cmpErr.Differing) != 1 || cmpErr.Differing[0] != "corpus_id" {
		t.Errorf("expected corpus_id to be named, got %v", cmpErr.Differing)
	}
}

// TestCaptureEnvironmentReportsWhatItCannotMeasure confirms capture leaves
// undeclarable fields empty rather than filling them with a plausible guess,
// and that the result is then refused until declared.
func TestCaptureEnvironmentReportsWhatItCannotMeasure(t *testing.T) {
	dir := t.TempDir()

	bare := CaptureEnvironment(dir, EnvOptions{})
	if err := bare.Validate(); err == nil {
		t.Fatal("an environment with nothing declared should not validate")
	}
	if bare.OS == "" || bare.Arch == "" || bare.GoVersion == "" {
		t.Error("capture failed to record the fields it can always measure")
	}
	if bare.KernelVersion == "" {
		t.Error("kernel version should be a stated value, never empty")
	}

	declared := CaptureEnvironment(dir, EnvOptions{
		DeviceClass:    DeviceUnknown,
		CacheState:     CacheWarm,
		Workers:        1,
		CorpusID:       "wide/scale=10/seed=0/size=64",
		CorpusCoverage: "all requested shape elements present",
	})
	if declared.FilesystemType == "" {
		t.Skip("filesystem type not measurable on this platform; declaration required")
	}
	if err := declared.Validate(); err != nil {
		t.Errorf("a fully declared environment was refused: %v", err)
	}
	if declared.FilesystemTypeSource != SourceMeasured {
		t.Errorf("measured filesystem type attributed as %q", declared.FilesystemTypeSource)
	}
	if declared.DeviceClassSource != SourceDeclared {
		t.Errorf("declared device class attributed as %q", declared.DeviceClassSource)
	}
}

// TestColdClaimRecordsWhetherPurgeSucceeded checks that a cold-cache row whose
// purge failed says so, rather than presenting an unenforced assertion as a
// controlled condition.
func TestColdClaimRecordsWhetherPurgeSucceeded(t *testing.T) {
	env := completeEnv()
	env.CacheState = CacheCold
	env.CachePurgeAttempted = true
	env.CachePurgeSucceeded = false

	if err := env.Validate(); err != nil {
		t.Fatalf("row should still be emittable: %v", err)
	}
	if !strings.Contains(env.Summary(), "purge failed") {
		t.Errorf("summary hides the failed purge: %q", env.Summary())
	}

	env.CachePurgeSucceeded = true
	if strings.Contains(env.Summary(), "purge failed") {
		t.Errorf("summary reports a failed purge for a successful one: %q", env.Summary())
	}
}

// TestUnreportedCountYieldsNoRate covers a defect this harness produced in its
// own first real run: du reports no entry count, EntriesExamined is -1, and
// dividing that by wall time printed a negative throughput. A negative rate
// looks like a measurement and sorts like one.
func TestUnreportedCountYieldsNoRate(t *testing.T) {
	res := okResult()
	res.EntriesExamined = -1

	if got := res.EntriesPerSecond(); got != 0 {
		t.Errorf("unreported entry count produced a rate of %v, want 0", got)
	}

	m, err := NewMeasurement("du", "1.0", 0, completeEnv(), res, CountNotReported, WorkPathsOnly)
	if err != nil {
		t.Fatalf("row rejected: %v", err)
	}
	if m.Derived.EntriesPerSecond < 0 {
		t.Errorf("derived rate is negative: %v", m.Derived.EntriesPerSecond)
	}
	if m.Derived.EntryCountSemantics != CountNotReported {
		t.Errorf("row does not state that its count is unreported: %q", m.Derived.EntryCountSemantics)
	}
}

// TestCountSemanticsRequired confirms a row cannot omit what its entry count
// means.
func TestCountSemanticsRequired(t *testing.T) {
	if _, err := NewMeasurement("find", "1.0", 0, completeEnv(), okResult(), "", WorkMetadata); err == nil {
		t.Error("accepted a measurement that does not state its count semantics")
	}
}

// TestNoiseFloorMarksIndistinguishableRuns confirms a run too short to
// distinguish implementations is marked rather than silently comparable.
func TestNoiseFloorMarksIndistinguishableRuns(t *testing.T) {
	fast := okResult()
	fast.WallTime = 5 * time.Millisecond
	if !fast.BelowNoiseFloor() {
		t.Error("a 5ms run should be marked below the noise floor")
	}

	slow := okResult()
	slow.WallTime = 2 * time.Second
	if slow.BelowNoiseFloor() {
		t.Error("a 2s run should not be marked below the noise floor")
	}

	m, err := NewMeasurement("walkdir-serial", "dev", 0, completeEnv(), fast, CountEntriesVisited, WorkMetadata)
	if err != nil {
		t.Fatalf("row rejected: %v", err)
	}
	if !m.Derived.BelowNoiseFloor {
		t.Error("short run not marked in the emitted row")
	}
}
