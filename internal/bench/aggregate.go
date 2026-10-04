package bench

import (
	"fmt"
	"sort"
	"time"
)

// Group identifies a set of rows that differ only by repetition: the same tool
// measured the same way, under the same conditions, more than once.
type Group struct {
	Tool        string      `json:"tool"`
	ToolVersion string      `json:"tool_version"`
	CorpusID    string      `json:"corpus_id"`
	CacheState  CacheState  `json:"cache_state"`
	Workers     int         `json:"workers"`
	WorkMode    WorkMode    `json:"work_mode"`
	DeviceClass DeviceClass `json:"device_class"`
	Filesystem  string      `json:"filesystem_type"`
}

// Aggregate summarizes repeated measurements of one group.
//
// The median is reported rather than the mean because a benchmark's outliers
// are one-sided: a run can be arbitrarily slowed by something else on the
// machine, but nothing makes it faster than the work takes. A mean lets one
// scheduler hiccup move the headline number; a median does not.
//
// Minimum and spread travel with it, because a median alone hides whether the
// runs agreed. Two implementations whose medians differ by less than either
// one's spread have not been distinguished.
type Aggregate struct {
	Group Group `json:"group"`

	// Repetitions is how many rows were combined.
	Repetitions int `json:"repetitions"`

	// WallMedian, WallMin, and WallMax describe elapsed time.
	WallMedian time.Duration `json:"wall_median_ns"`
	WallMin    time.Duration `json:"wall_min_ns"`
	WallMax    time.Duration `json:"wall_max_ns"`

	// TTFRMedian is the median time to first result.
	TTFRMedian time.Duration `json:"ttfr_median_ns"`

	// EntriesExamined is the count observed. Repeated runs over an unchanged
	// corpus must agree; see CountsAgree.
	EntriesExamined int64 `json:"entries_examined"`

	// CountsAgree reports whether every repetition examined the same number
	// of entries. Disagreement means the tree changed under measurement or
	// the implementation is nondeterministic, and either way the timings
	// describe different work.
	CountsAgree bool `json:"counts_agree"`

	// Gaps is the gap count observed, and GapsAgree the same check.
	Gaps      int64 `json:"gaps"`
	GapsAgree bool  `json:"gaps_agree"`

	// PeakRSSMax is the largest peak resident set size seen.
	PeakRSSMax int64 `json:"peak_rss_max_bytes"`

	// Semantics states what EntriesExamined counts.
	Semantics CountSemantics `json:"entry_count_semantics"`

	// WorkMode states what the runs did per entry.
	WorkMode WorkMode `json:"work_mode"`

	// BelowNoiseFloor marks a group whose median run is too short to
	// distinguish implementations.
	BelowNoiseFloor bool `json:"below_noise_floor"`

	// ColdUnenforced marks a group labelled cold whose page-cache purge did
	// not actually happen. Its timings are not cold-cache evidence.
	ColdUnenforced bool `json:"cold_unenforced"`

	// PurgeDetail explains an unenforced cold claim.
	PurgeDetail string `json:"purge_detail,omitempty"`

	// Outcomes counts how each repetition ended.
	Outcomes map[Outcome]int `json:"outcomes"`
}

// Spread returns the difference between the slowest and fastest run, as a
// fraction of the median. A group whose spread exceeds the difference between
// two implementations has not distinguished them.
func (a Aggregate) Spread() float64 {
	if a.WallMedian <= 0 {
		return 0
	}
	return float64(a.WallMax-a.WallMin) / float64(a.WallMedian)
}

// EntriesPerSecond returns the median-based entry rate, or zero when the count
// is unreported or the runs disagreed about it.
func (a Aggregate) EntriesPerSecond() float64 {
	if a.WallMedian <= 0 || a.EntriesExamined < 0 || !a.CountsAgree {
		return 0
	}
	return float64(a.EntriesExamined) / a.WallMedian.Seconds()
}

// Trustworthy reports whether this group's timing can carry a comparison.
//
// It is deliberately conservative. A group fails if its runs are too short to
// distinguish implementations, if they disagreed about how much work they did,
// if any run did not complete, or if it claims a cold cache it did not get.
// Each of those makes the median a number about something other than the
// implementation's speed.
func (a Aggregate) Trustworthy() (bool, []string) {
	var reasons []string
	if a.BelowNoiseFloor {
		reasons = append(reasons, "median run below the noise floor")
	}
	if !a.CountsAgree {
		reasons = append(reasons, "repetitions examined different entry counts")
	}
	if !a.GapsAgree {
		reasons = append(reasons, "repetitions reported different gap counts")
	}
	if a.ColdUnenforced {
		reasons = append(reasons, "cold cache claimed but not enforced")
	}
	if a.Outcomes[OutcomeFailed] > 0 {
		reasons = append(reasons, fmt.Sprintf("%d repetition(s) failed", a.Outcomes[OutcomeFailed]))
	}
	if a.Repetitions < 3 {
		reasons = append(reasons, fmt.Sprintf("only %d repetition(s)", a.Repetitions))
	}
	return len(reasons) == 0, reasons
}

// Aggregated groups a set's rows and summarizes each group.
func (s *Set) Aggregated() []Aggregate {
	byGroup := map[Group][]*Measurement{}
	for _, row := range s.rows {
		g := Group{
			Tool:        row.Tool,
			ToolVersion: row.ToolVersion,
			CorpusID:    row.Environment.CorpusID,
			CacheState:  row.Environment.CacheState,
			Workers:     row.Environment.Workers,
			WorkMode:    row.Derived.WorkMode,
			DeviceClass: row.Environment.DeviceClass,
			Filesystem:  row.Environment.FilesystemType,
		}
		byGroup[g] = append(byGroup[g], row)
	}

	out := make([]Aggregate, 0, len(byGroup))
	for g, rows := range byGroup {
		out = append(out, summarizeGroup(g, rows))
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Group.CorpusID != out[j].Group.CorpusID {
			return out[i].Group.CorpusID < out[j].Group.CorpusID
		}
		if out[i].Group.CacheState != out[j].Group.CacheState {
			return out[i].Group.CacheState < out[j].Group.CacheState
		}
		if out[i].Group.Tool != out[j].Group.Tool {
			return out[i].Group.Tool < out[j].Group.Tool
		}
		return out[i].Group.Workers < out[j].Group.Workers
	})
	return out
}

func summarizeGroup(g Group, rows []*Measurement) Aggregate {
	agg := Aggregate{
		Group:       g,
		Repetitions: len(rows),
		Outcomes:    map[Outcome]int{},
		CountsAgree: true,
		GapsAgree:   true,
		Semantics:   rows[0].Derived.EntryCountSemantics,
		WorkMode:    rows[0].Derived.WorkMode,
	}

	walls := make([]time.Duration, 0, len(rows))
	ttfrs := make([]time.Duration, 0, len(rows))

	for i, row := range rows {
		walls = append(walls, row.Result.WallTime)
		ttfrs = append(ttfrs, row.Result.TimeToFirstResult)
		agg.Outcomes[row.Result.Outcome]++

		if row.Result.PeakRSSBytes > agg.PeakRSSMax {
			agg.PeakRSSMax = row.Result.PeakRSSBytes
		}
		if i == 0 {
			agg.EntriesExamined = row.Result.EntriesExamined
			agg.Gaps = row.Result.Gaps
		} else {
			if row.Result.EntriesExamined != agg.EntriesExamined {
				agg.CountsAgree = false
			}
			if row.Result.Gaps != agg.Gaps {
				agg.GapsAgree = false
			}
		}

		env := row.Environment
		if env.CacheState == CacheCold && !env.CachePurgeSucceeded {
			agg.ColdUnenforced = true
			if agg.PurgeDetail == "" {
				agg.PurgeDetail = env.CachePurgeDetail
			}
		}
	}

	agg.WallMedian = median(walls)
	agg.WallMin, agg.WallMax = minMax(walls)
	agg.TTFRMedian = median(ttfrs)
	agg.BelowNoiseFloor = agg.WallMedian > 0 && agg.WallMedian < NoiseFloor

	return agg
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func minMax(values []time.Duration) (minimum, maximum time.Duration) {
	if len(values) == 0 {
		return 0, 0
	}
	minimum, maximum = values[0], values[0]
	for _, v := range values[1:] {
		if v < minimum {
			minimum = v
		}
		if v > maximum {
			maximum = v
		}
	}
	return minimum, maximum
}
