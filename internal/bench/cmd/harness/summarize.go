package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/3leaps/spanwit/internal/bench"
)

// runSummarize groups repeated rows and reports each group's median, spread,
// and whether it can carry a comparison at all.
//
// The trust column is the point. A table of medians invites the reader to
// compare the two smallest, and most of the reasons a comparison is invalid —
// runs below the noise floor, repetitions that did different amounts of work,
// a cold cache that was never enforced — are invisible in the median itself.
func runSummarize(args []string) error {
	fs := flag.NewFlagSet("summarize", flag.ContinueOnError)
	in := fs.String("in", "", "JSONL rows file to summarize (required)")
	onlyTrustworthy := fs.Bool("only-trustworthy", false,
		"omit groups that cannot carry a comparison instead of marking them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("summarize requires --in")
	}

	set, err := loadRows(*in)
	if err != nil {
		return err
	}

	aggs := set.Aggregated()
	fmt.Printf("%-18s %-20s %-5s %-10s %4s %4s %9s %8s %9s %10s  %s\n",
		"TOOL", "CORPUS", "CACHE", "WORK", "WKR", "REP", "MEDIAN_MS", "SPREAD", "TTFR_MS", "ENT/SEC", "TRUST")

	var untrusted int
	for _, a := range aggs {
		ok, reasons := a.Trustworthy()
		if !ok {
			untrusted++
			if *onlyTrustworthy {
				continue
			}
		}

		rate := "-"
		if r := a.EntriesPerSecond(); r > 0 {
			rate = fmt.Sprintf("%.0f", r)
		}
		trust := "yes"
		if !ok {
			trust = strings.Join(reasons, "; ")
		}

		fmt.Printf("%-18s %-20s %-5s %-10s %4d %4d %9.1f %7.1f%% %9.2f %10s  %s\n",
			a.Group.Tool,
			shorten(a.Group.CorpusID, 20),
			a.Group.CacheState,
			a.Group.WorkMode,
			a.Group.Workers,
			a.Repetitions,
			float64(a.WallMedian.Nanoseconds())/1e6,
			a.Spread()*100,
			float64(a.TTFRMedian.Nanoseconds())/1e6,
			rate,
			trust)
	}

	fmt.Fprintf(os.Stderr, "\n%d group(s); %d cannot carry a comparison\n", len(aggs), untrusted)
	return nil
}

// loadRows reads a JSONL measurement file.
func loadRows(path string) (*bench.Set, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied results path
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	set := &bench.Set{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row bench.Measurement
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("parse row in %s: %w", path, err)
		}
		set.Add(&row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return set, nil
}

// shorten trims a long identifier for column display.
func shorten(s string, width int) string {
	if len(s) <= width {
		return s
	}
	return s[:width-1] + "…"
}
