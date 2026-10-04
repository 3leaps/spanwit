package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Outcome describes how a measured run ended. A run that did not complete is
// still worth recording; presenting its throughput as a completed walk's is
// not.
type Outcome string

const (
	// OutcomeComplete means the implementation traversed everything it was
	// able to reach and terminated normally.
	OutcomeComplete Outcome = "complete"

	// OutcomePartial means the run finished but could not reach part of the
	// tree, typically through permission denials.
	OutcomePartial Outcome = "partial"

	// OutcomeFailed means the run did not finish.
	OutcomeFailed Outcome = "failed"
)

// Result holds the raw numbers a single run produced, before they are bound to
// an environment.
type Result struct {
	// TimeToFirstResult is the latency until the implementation produced its
	// first matching entry. For a user staring at a full disk this matters
	// more than total wall time, and it is the number a batch-then-print
	// implementation is worst at.
	TimeToFirstResult time.Duration `json:"time_to_first_result_ns"`

	// WallTime is the total elapsed time to the terminal summary.
	WallTime time.Duration `json:"wall_time_ns"`

	// CPUTime is user plus system time consumed by the run.
	CPUTime time.Duration `json:"cpu_time_ns"`

	// EntriesExamined counts filesystem entries the implementation looked
	// at, whether or not they matched.
	EntriesExamined int64 `json:"entries_examined"`

	// EntriesMatched counts entries that satisfied the run's filters.
	//
	// Kept distinct from EntriesExamined and from anything emitted: matched
	// is not emitted when a top-K limit applies, and conflating the three is
	// how a tool ends up reporting a number labelled as something it does
	// not measure.
	EntriesMatched int64 `json:"entries_matched"`

	// EntriesEmitted counts entries the implementation actually wrote out.
	EntriesEmitted int64 `json:"entries_emitted"`

	// ApparentBytes sums the apparent size of matched entries.
	//
	// This is not reclaimable space and must never be labelled as such:
	// hard-linked entries are counted once per directory entry and sparse
	// files report more than they allocate.
	ApparentBytes int64 `json:"apparent_bytes"`

	// Gaps counts places the implementation could not read, such as denied
	// directories. A run reporting zero gaps over a corpus known to contain
	// sealed directories has miscounted or is not reporting them.
	Gaps int64 `json:"gaps"`

	// PeakRSSBytes is the maximum resident set size observed.
	PeakRSSBytes int64 `json:"peak_rss_bytes"`

	// PeakOpenFDs is the maximum open file descriptor count observed, or -1
	// when the platform does not expose it. Descriptor exhaustion is a
	// bounded-resource failure a throughput number alone will not reveal.
	PeakOpenFDs int64 `json:"peak_open_fds"`

	// BytesWrittenByMount records write deltas per mounted volume across the
	// run.
	//
	// This is benchmark evidence about the whole system, not the guarantee
	// itself: filesystem metadata writeback and a caller's own stdout
	// redirection both land here without the implementation having written
	// anything. The application-level zero-write promise is tested
	// separately, by asserting the implementation creates no file, temp,
	// cache, or database of its own.
	BytesWrittenByMount map[string]int64 `json:"bytes_written_by_mount,omitempty"`

	// Outcome describes how the run ended.
	Outcome Outcome `json:"outcome"`

	// Err holds the failure text when Outcome is OutcomeFailed.
	Err string `json:"error,omitempty"`
}

// EntriesPerSecond returns examined entries per second of wall time.
//
// It returns zero when the run reported no entry count at all. EntriesExamined
// is -1 for comparators whose output does not map onto entries, and dividing
// that by elapsed time yields a negative throughput: a number that looks like a
// measurement, sorts like one, and measures nothing. Refusing to compute it is
// the same rule as refusing to emit an unenvironmented row.
func (r Result) EntriesPerSecond() float64 {
	if r.WallTime <= 0 || r.EntriesExamined < 0 {
		return 0
	}
	return float64(r.EntriesExamined) / r.WallTime.Seconds()
}

// NoiseFloor is the wall time below which a run's timing is dominated by
// process startup and scheduling rather than by traversal.
//
// On the order of a few milliseconds, forking a process and linking a Go
// runtime costs more than walking a small tree, so two implementations
// separated by less than this are not distinguishable by it. Rows below the
// floor are still emitted — they are real measurements of real runs — but they
// are marked, because the failure mode is not a wrong number, it is a correct
// number used to support a comparison it cannot carry.
const NoiseFloor = 50 * time.Millisecond

// BelowNoiseFloor reports whether the run was too short for its wall time to
// distinguish implementations.
func (r Result) BelowNoiseFloor() bool {
	return r.WallTime > 0 && r.WallTime < NoiseFloor
}

// Measurement is one row: a result bound to the environment it was taken in.
// It is the only shape in which a number leaves this package.
type Measurement struct {
	// Tool identifies the measured implementation, such as "walkdir-serial",
	// "find", or "gdu".
	Tool string `json:"tool"`

	// ToolVersion identifies the build measured. External comparators change
	// between releases, and a comparison against an unnamed version is not
	// reproducible.
	ToolVersion string `json:"tool_version"`

	// Repetition distinguishes repeated runs of an otherwise identical row.
	Repetition int `json:"repetition"`

	// Environment states the conditions. Never empty: see NewMeasurement.
	Environment Environment `json:"environment"`

	// Result holds the numbers.
	Result Result `json:"result"`

	// Derived carries computed convenience figures so a consumer reading
	// JSONL does not recompute them inconsistently.
	Derived Derived `json:"derived"`
}

// Derived holds figures computed from Result, recorded so every consumer
// agrees on them.
type Derived struct {
	EntriesPerSecond float64 `json:"entries_per_second"`
	WallTimeMillis   float64 `json:"wall_time_ms"`
	TTFRMillis       float64 `json:"time_to_first_result_ms"`

	// EntryCountSemantics states what the entry count in this row means, so
	// a reader does not compare a filtered match count against a count of
	// every entry visited.
	EntryCountSemantics CountSemantics `json:"entry_count_semantics"`

	// WorkMode states what the run did per entry. A paths-only run and a
	// metadata run are not comparable on wall time, however alike their
	// entry counts look.
	WorkMode WorkMode `json:"work_mode"`

	// BelowNoiseFloor marks a run too short for its wall time to
	// distinguish implementations.
	BelowNoiseFloor bool `json:"below_noise_floor"`
}

// NewMeasurement binds a result to its environment, refusing to produce a row
// whose environment is incomplete.
//
// This refusal is the mechanism. Everything else in this package is a
// convenience; this is the part that makes an unenvironmented number
// impossible to emit rather than merely discouraged.
func NewMeasurement(
	tool, toolVersion string,
	repetition int,
	env Environment,
	res Result,
	semantics CountSemantics,
	mode WorkMode,
) (*Measurement, error) {
	if tool == "" {
		return nil, fmt.Errorf("bench: measurement requires a tool name")
	}
	if toolVersion == "" {
		return nil, fmt.Errorf("bench: measurement for %q requires a tool version", tool)
	}
	if repetition < 0 {
		return nil, fmt.Errorf("bench: measurement for %q has negative repetition %d", tool, repetition)
	}
	if err := env.Validate(); err != nil {
		return nil, fmt.Errorf("bench: cannot record %q: %w", tool, err)
	}
	if res.Outcome == "" {
		return nil, fmt.Errorf("bench: measurement for %q has no outcome", tool)
	}
	if semantics == "" {
		// An entry count whose meaning is unstated invites exactly the
		// comparison it cannot support.
		return nil, fmt.Errorf("bench: measurement for %q does not state its count semantics", tool)
	}
	if mode == "" {
		return nil, fmt.Errorf("bench: measurement for %q does not state its work mode", tool)
	}

	return &Measurement{
		Tool:        tool,
		ToolVersion: toolVersion,
		Repetition:  repetition,
		Environment: env,
		Result:      res,
		Derived: Derived{
			EntriesPerSecond:    res.EntriesPerSecond(),
			WallTimeMillis:      float64(res.WallTime.Nanoseconds()) / 1e6,
			TTFRMillis:          float64(res.TimeToFirstResult.Nanoseconds()) / 1e6,
			EntryCountSemantics: semantics,
			WorkMode:            mode,
			BelowNoiseFloor:     res.BelowNoiseFloor(),
		},
	}, nil
}

// Set is an ordered collection of measurements.
type Set struct {
	rows []*Measurement
}

// Add appends a measurement.
func (s *Set) Add(m *Measurement) {
	if m != nil {
		s.rows = append(s.rows, m)
	}
}

// Rows returns the measurements in insertion order.
func (s *Set) Rows() []*Measurement {
	out := make([]*Measurement, len(s.rows))
	copy(out, s.rows)
	return out
}

// Len returns the number of rows.
func (s *Set) Len() int { return len(s.rows) }

// WriteJSONL writes one measurement per line.
func (s *Set) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, row := range s.rows {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("bench: encode measurement for %q: %w", row.Tool, err)
		}
	}
	return nil
}

// ComparisonError reports rows that were compared while differing in more than
// the swept dimension.
type ComparisonError struct {
	ToolA, ToolB string
	Differing    []string
}

func (e *ComparisonError) Error() string {
	return fmt.Sprintf("bench: %q and %q differ in %v beyond the swept dimension",
		e.ToolA, e.ToolB, e.Differing)
}

// CheckComparable verifies every row in the set shares an environment with the
// first, except in the named swept dimensions.
//
// Call this before presenting a set as a comparison. Two rows from different
// filesystems placed side by side under a "faster" heading is a claim about
// the implementations that the evidence does not support, and it is easier to
// make by accident than on purpose.
func (s *Set) CheckComparable(swept ...string) error {
	if len(s.rows) < 2 {
		return nil
	}
	base := s.rows[0]
	for _, row := range s.rows[1:] {
		ok, differing := base.Environment.Comparable(row.Environment, swept...)
		if !ok {
			return &ComparisonError{
				ToolA:     base.Tool,
				ToolB:     row.Tool,
				Differing: differing,
			}
		}
	}
	return nil
}
