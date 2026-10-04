// Package inventory provides Spanwit's neutral, read-only filesystem
// enumeration engine.
//
// Discovery is not deletion authority. This package observes regular files and
// reports coverage; it has no mutation or prune-plan path.
package inventory

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ProfileV0            = "spanwit.filesystem-inventory/v0"
	AggregationProfileV0 = "spanwit.filesystem-inventory-aggregation/v0"
	AggregationProfileV1 = "spanwit.filesystem-inventory-aggregation/v1"

	BackendAuto     = "auto"
	BackendSerial   = "serial"
	BackendParallel = "parallel"

	DefaultMaxPendingDirs          = 65536
	DefaultMaxAggregateDirectories = 250000

	SizeApparent  = "apparent"
	SizeAllocated = "allocated"

	LifecycleComplete = "complete"
	LifecyclePartial  = "partial"
	LifecycleFailed   = "failed"

	EmissionEntries          = "entries"
	EmissionSummaryOnly      = "summary_only"
	EmissionDirectorySummary = "directory_summary"

	PathProtectionSourceStructure = "source_structure:block_export"
)

// Accounting claim vocabulary for the v1 directory-accounting profile.
// Observed sums are per-path-entry measurements; unsupported planes carry no
// number because portable public metadata cannot observe them.
const (
	ClaimStatusMeasured    = "measured"
	ClaimStatusPartial     = "partial"
	ClaimStatusUnsupported = "unsupported"

	BasisApparentPathEntrySum  = "path_entry_logical_size_sum"
	BasisAllocatedPathEntrySum = "path_entry_allocated_blocks_sum"

	BoundExact = "exact"
	BoundLower = "lower"
)

// Options controls one inventory run.
type Options struct {
	Roots []string
	// Traversal opts summary-only observation consumers into bounded discovery.
	// Nil preserves inventory/diagnose traversal and root admission.
	Traversal               *TraversalOptions
	Backend                 string
	Workers                 int
	MaxOpenDirs             int
	MaxPendingDirs          int
	MinSize                 int64
	OlderThan               time.Duration
	NewerThan               time.Duration
	SizeBasis               string
	OneFilesystem           bool
	Top                     int
	Exclusions              []string
	EmissionMode            string
	DirectoryDepth          int
	DirectoryTop            int
	MaxAggregateDirectories int
	// DirectoryAccounting opts in to the v1 accounting projection: typed
	// byte-plane claims on every directory record. It requires
	// EmissionDirectorySummary and changes the profile to
	// spanwit.filesystem-inventory-aggregation/v1.
	DirectoryAccounting bool

	// DirectoryFloor is the inclusive directory-audit size floor in bytes over
	// the selected basis. Nil means no floor; an explicit zero is applied. It
	// requires EmissionDirectoryAudit.
	DirectoryFloor *int64

	// auditHooks are deterministic test barriers; nil in production.
	auditHooks *auditHooks

	// MutationContract is the effective invocation mutation contract for this
	// run (open | read_only). Empty normalizes to open.
	MutationContract string

	// IncludeRemote walks network-filesystem and cloud-placeholder (File
	// Provider) directories found below a root. Off by default: opening them
	// can download content or block. An explicitly named root is always walked.
	IncludeRemote bool

	// StallTimeout bounds how long one directory open may block before its
	// subtree is skipped as a path-free "stalled" gap. Zero uses
	// DefaultStallTimeout; a negative value never abandons an open.
	StallTimeout time.Duration

	// OnStall receives a coalesced, path-free alert when directory opens stop
	// responding. It is called from walker goroutines and must be safe for
	// concurrent use. Nil disables alerts.
	OnStall func(StallAlert)

	// stallAlertAfter overrides the alert delay in tests (zero = default).
	stallAlertAfter time.Duration

	// Progress receives a bounded, path-free operational snapshot. It is called
	// synchronously at most once per ProgressInterval; a nil callback disables
	// progress reporting. Zero uses a one-second interval.
	Progress         func(Progress)
	ProgressInterval time.Duration

	// Now is captured once for every age comparison. Zero means time.Now().
	Now time.Time

	// RunID is injectable for deterministic tests. Empty creates a local,
	// non-secret run identifier.
	RunID string
}

// Mutation contract labels (shared with CLI assertion policy).
const (
	MutationContractOpen     = "open"
	MutationContractReadOnly = "read_only"
)

// Root is an admitted enumeration root.
type Root struct {
	ID       string `json:"id"`
	Path     string `json:"local_path"`
	DeviceID string `json:"device_id,omitempty"`
}

// Entry is one regular file that passed every declared filter.
type Entry struct {
	RootID             string    `json:"root_id"`
	RelativePath       string    `json:"relative_path"`
	LocalAbsolutePath  string    `json:"local_absolute_path"`
	EntryType          string    `json:"entry_type"`
	ApparentSizeBytes  int64     `json:"apparent_size_bytes"`
	AllocatedSizeBytes *int64    `json:"allocated_size_bytes,omitempty"`
	ModifiedAt         time.Time `json:"modified_at"`
	ObservedAt         time.Time `json:"observed_at"`
	DeviceID           string    `json:"device_id,omitempty"`
	FileID             string    `json:"file_id,omitempty"`
	MetadataSource     string    `json:"metadata_source"`
	SymlinkFollowed    bool      `json:"symlink_followed"`
}

// Directory is one post-walk aggregate over an admitted directory's full
// declared subject. Byte values are per-path-entry sums, never reclaimable or
// unique physical bytes. Accounting is populated only under the v1
// directory-accounting profile.
type Directory struct {
	RootID                   string               `json:"root_id"`
	RelativePath             string               `json:"relative_path"`
	Depth                    int                  `json:"depth"`
	ApparentBytesSum         int64                `json:"apparent_bytes_sum"`
	AllocatedBytesSum        *int64               `json:"allocated_bytes_sum,omitempty"`
	AllocatedUnmeasuredCount int64                `json:"allocated_unmeasured_count"`
	FileCount                int64                `json:"file_count"`
	DescendantDirectoryCount int64                `json:"descendant_directory_count"`
	Lifecycle                string               `json:"lifecycle"`
	AffectingGapCount        int64                `json:"affecting_gap_count"`
	Accounting               *DirectoryAccounting `json:"accounting,omitempty"`
}

// AccountingClaim is one typed byte-plane observation. Observed claims carry
// basis, bound, and bytes; unsupported claims carry only a detail and no
// number. Unknown values are omitted, never serialized as zero.
type AccountingClaim struct {
	Status          string `json:"status"`
	Basis           string `json:"basis,omitempty"`
	Bound           string `json:"bound,omitempty"`
	Bytes           *int64 `json:"bytes,omitempty"`
	UnmeasuredCount *int64 `json:"unmeasured_count,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// DirectoryAccounting carries the v1 byte-plane claims for one directory
// aggregate. Unique physical, shared/cloned, and expected-reclaim bytes are
// always unsupported here: no portable public metadata observes them, and no
// observed sum becomes deletion authority.
type DirectoryAccounting struct {
	Apparent        AccountingClaim `json:"apparent"`
	Allocated       AccountingClaim `json:"allocated"`
	UniquePhysical  AccountingClaim `json:"unique_physical"`
	SharedCloned    AccountingClaim `json:"shared_cloned"`
	ExpectedReclaim AccountingClaim `json:"expected_reclaim"`
}

// AggregateSubject states that directory accounting is independent of file
// emission predicates.
type AggregateSubject struct {
	AccountingScope  string `json:"accounting_scope"`
	EntryType        string `json:"entry_type"`
	SymlinkPosture   string `json:"symlink_posture"`
	RankingSizeBasis string `json:"ranking_size_basis"`
}

// SelectedSize returns the value used for filtering and top-K ranking.
func (e Entry) SelectedSize(basis string) (int64, bool) {
	if basis == SizeAllocated {
		if e.AllocatedSizeBytes == nil {
			return 0, false
		}
		return *e.AllocatedSizeBytes, true
	}
	return e.ApparentSizeBytes, true
}

// Gap is an explicit coverage omission.
type Gap struct {
	RootID              string `json:"root_id"`
	RelativePath        string `json:"relative_path,omitempty"`
	LocalAbsolutePath   string `json:"local_absolute_path,omitempty"`
	Kind                string `json:"kind"`
	Detail              string `json:"detail,omitempty"`
	AffectsCompleteness bool   `json:"affects_completeness"`
}

// Exclusion is a normalized rule defining content outside the declared
// subject. Basename rules match at every depth; root-relative rules only match
// their exact normalized relative path.
type Exclusion struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Header states the declared scope before any entry is emitted.
type Header struct {
	Traversal               *TraversalScope `json:"traversal,omitempty"`
	RunID                   string          `json:"-"`
	CapturedAt              time.Time       `json:"-"`
	Profile                 string          `json:"profile"`
	Roots                   []Root          `json:"roots"`
	Filters                 FilterSummary   `json:"filters"`
	Backend                 string          `json:"backend"`
	Workers                 int             `json:"workers"`
	MaxOpenDirs             int             `json:"max_open_dirs"`
	MaxPendingDirs          int             `json:"max_pending_dirs"`
	OneFilesystem           bool            `json:"one_filesystem"`
	Top                     int             `json:"top"`
	EmissionMode            string          `json:"emission_mode"`
	DirectoryDepth          *int            `json:"directory_depth,omitempty"`
	DirectoryTop            *int            `json:"directory_top,omitempty"`
	MaxAggregateDirectories *int            `json:"max_aggregate_directories,omitempty"`
	// DirectoryAccounting is present only under the v1 accounting profile.
	DirectoryAccounting *bool             `json:"directory_accounting,omitempty"`
	Subject             *AggregateSubject `json:"subject,omitempty"`
	Exclusions          []Exclusion       `json:"exclusions"`
	Ordering            string            `json:"ordering"`
	PathProtection      string            `json:"path_protection"`
	// MutationContract is the effective no-mutation assertion for this run.
	MutationContract string `json:"mutation_contract"`
}

// RootSummary is reconciled terminal accounting for one admitted root.
// Lifecycle is never stronger than the observations gathered for that root.
type RootSummary struct {
	RootID string `json:"root_id"`
	// Observed distinguishes measured empty roots from roots never read.
	// Populated only for opt-in observation traversal.
	Observed                 *bool  `json:"observed,omitempty"`
	Lifecycle                string `json:"lifecycle"`
	VisitedEntries           int64  `json:"visited_entries"`
	VisitedDirectories       int64  `json:"visited_directories"`
	MatchedCount             int64  `json:"matched_count"`
	EmittedCount             int64  `json:"emitted_count"`
	EmittedApparentBytes     int64  `json:"emitted_apparent_bytes"`
	EmittedAllocatedBytes    int64  `json:"emitted_allocated_bytes"`
	EmittedUnmeasuredCount   int64  `json:"emitted_unmeasured_count"`
	MatchedApparentBytes     int64  `json:"matched_apparent_bytes"`
	MatchedAllocatedBytes    int64  `json:"matched_allocated_bytes"`
	AllocatedUnmeasuredCount int64  `json:"allocated_unmeasured_count"`
	GapCount                 int64  `json:"gap_count"`
	BoundarySkipCount        int64  `json:"boundary_skip_count"`
	ExclusionCount           int64  `json:"exclusion_count"`
	VanishedCount            int64  `json:"vanished_count"`
	QueueLimitSkipCount      int64  `json:"queue_limit_skip_count"`
	RemoteSkipCount          int64  `json:"remote_skip_count,omitempty"`
	StalledCount             int64  `json:"stalled_count,omitempty"`
	Canceled                 bool   `json:"canceled"`
	Failed                   bool   `json:"failed"`
}

// FilterSummary is the fully resolved predicate.
type FilterSummary struct {
	EntryType      string `json:"entry_type"`
	MinSizeBytes   int64  `json:"min_size_bytes"`
	SizeBasis      string `json:"size_basis"`
	OlderThanNanos int64  `json:"older_than_nanos,omitempty"`
	NewerThanNanos int64  `json:"newer_than_nanos,omitempty"`
	CapturedAt     string `json:"captured_at"`
}

// Summary is terminal producer accounting.
type Summary struct {
	Lifecycle                   string        `json:"lifecycle"`
	Backend                     string        `json:"backend"`
	VisitedEntries              int64         `json:"visited_entries"`
	VisitedDirectories          int64         `json:"visited_directories"`
	MatchedCount                int64         `json:"matched_count"`
	EmittedCount                int64         `json:"emitted_count"`
	EmittedApparentBytes        int64         `json:"emitted_apparent_bytes"`
	EmittedAllocatedBytes       int64         `json:"emitted_allocated_bytes"`
	EmittedUnmeasuredCount      int64         `json:"emitted_unmeasured_count"`
	MatchedApparentBytes        int64         `json:"matched_apparent_bytes"`
	MatchedAllocatedBytes       int64         `json:"matched_allocated_bytes"`
	AllocatedUnmeasuredCount    int64         `json:"allocated_unmeasured_count"`
	GapCount                    int64         `json:"gap_count"`
	BoundarySkipCount           int64         `json:"boundary_skip_count"`
	VanishedCount               int64         `json:"vanished_count"`
	TimeToFirstMatchNanos       int64         `json:"time_to_first_match_nanos"`
	PeakDepth                   int64         `json:"peak_depth"`
	PeakPendingDirectories      int64         `json:"peak_pending_directories"`
	QueueLimitSkipCount         int64         `json:"queue_limit_skip_count"`
	RemoteSkipCount             int64         `json:"remote_skip_count,omitempty"`
	StalledCount                int64         `json:"stalled_count,omitempty"`
	ExclusionCount              int64         `json:"exclusion_count"`
	DurationNanos               int64         `json:"duration_nanos"`
	TerminalObservedAt          time.Time     `json:"terminal_observed_at"`
	Top                         int           `json:"top"`
	SelectionTruncated          bool          `json:"selection_truncated"`
	EmissionMode                string        `json:"emission_mode"`
	EntriesSuppressed           bool          `json:"entries_suppressed"`
	DirectoryEmittedCount       *int64        `json:"directory_emitted_count,omitempty"`
	DirectorySelectionTruncated *bool         `json:"directory_selection_truncated,omitempty"`
	Canceled                    bool          `json:"canceled"`
	Roots                       []RootSummary `json:"roots"`
}

// Progress is a path-free operational snapshot suitable for rate-limited CLI
// status output. Counts are projections only and never affect stream records.
type Progress struct {
	Elapsed            time.Duration
	VisitedEntries     int64
	VisitedDirectories int64
	MatchedCount       int64
	AffectingGapCount  int64
	ActiveRootID       string
	CompletedRoots     int
	TotalRoots         int
	EntriesPerSecond   float64
}

// Sink consumes the single inventory event stream. Calls are serialized even
// when the selected walker is parallel; a slow sink therefore applies bounded
// backpressure rather than creating unbounded memory.
type Sink interface {
	Header(Header) error
	Entry(Entry) error
	Gap(Gap) error
	Summary(Summary) error
}

// DirectorySink is required only by directory-summary runs, preserving the
// shipped v0 Sink interface for existing consumers.
type DirectorySink interface {
	Directory(Directory) error
}

type normalizedOptions struct {
	Options
	roots         []Root
	exclusions    []Exclusion
	now           time.Time
	stallTimeout  time.Duration // <= 0 means never abandon
	alertAfter    time.Duration
	admissionGaps map[string]Gap
}

func normalizeOptions(opts Options) (normalizedOptions, error) {
	if len(opts.Roots) == 0 {
		return normalizedOptions{}, errors.New("at least one explicit root is required")
	}
	if opts.MinSize < 0 {
		return normalizedOptions{}, errors.New("min size cannot be negative")
	}
	if opts.OlderThan < 0 || opts.NewerThan < 0 {
		return normalizedOptions{}, errors.New("age bounds cannot be negative")
	}
	if opts.OlderThan > 0 && opts.NewerThan > 0 && opts.OlderThan > opts.NewerThan {
		return normalizedOptions{}, fmt.Errorf(
			"older-than %s exceeds newer-than %s; the age window is empty",
			opts.OlderThan, opts.NewerThan)
	}
	if opts.Top < 0 {
		return normalizedOptions{}, errors.New("top cannot be negative")
	}
	if opts.DirectoryDepth < -1 {
		return normalizedOptions{}, errors.New("directory depth cannot be less than -1")
	}
	if opts.DirectoryTop < 0 {
		return normalizedOptions{}, errors.New("directory top cannot be negative")
	}
	switch opts.EmissionMode {
	case "", EmissionEntries:
		opts.EmissionMode = EmissionEntries
	case EmissionSummaryOnly, EmissionDirectorySummary, EmissionDirectoryAudit:
	default:
		return normalizedOptions{}, fmt.Errorf(
			"unsupported emission mode %q (use entries|summary_only|directory_summary|directory_audit)", opts.EmissionMode)
	}
	if opts.DirectoryFloor != nil {
		if opts.EmissionMode != EmissionDirectoryAudit {
			return normalizedOptions{}, errors.New("directory floor requires directory audit")
		}
		if *opts.DirectoryFloor < 0 {
			return normalizedOptions{}, errors.New("directory floor cannot be negative")
		}
	}
	if opts.EmissionMode == EmissionDirectoryAudit {
		if opts.Top != 0 || opts.MinSize != 0 || opts.OlderThan != 0 || opts.NewerThan != 0 {
			return normalizedOptions{}, errors.New("directory audit does not accept file selection predicates")
		}
		if opts.DirectoryAccounting {
			return normalizedOptions{}, errors.New("directory audit always carries accounting; directory accounting is a v1 option")
		}
		if opts.MaxAggregateDirectories <= 0 {
			return normalizedOptions{}, errors.New("max aggregate directories must be positive")
		}
	}
	if opts.EmissionMode == EmissionDirectorySummary {
		if opts.Top != 0 {
			return normalizedOptions{}, errors.New("directory summary is incompatible with file top")
		}
		if opts.MaxAggregateDirectories <= 0 {
			return normalizedOptions{}, errors.New("max aggregate directories must be positive")
		}
	}
	if opts.DirectoryAccounting {
		if opts.EmissionMode != EmissionDirectorySummary {
			return normalizedOptions{}, errors.New("directory accounting requires directory summary")
		}
	}
	if opts.ProgressInterval < 0 {
		return normalizedOptions{}, errors.New("progress interval cannot be negative")
	}
	if opts.ProgressInterval == 0 {
		opts.ProgressInterval = time.Second
	}
	switch opts.MutationContract {
	case "":
		opts.MutationContract = MutationContractOpen
	case MutationContractOpen, MutationContractReadOnly:
	default:
		return normalizedOptions{}, fmt.Errorf(
			"unsupported mutation contract %q (use open|read_only)", opts.MutationContract)
	}

	switch opts.SizeBasis {
	case "":
		opts.SizeBasis = SizeApparent
	case SizeApparent, SizeAllocated:
	default:
		return normalizedOptions{}, fmt.Errorf(
			"unsupported size basis %q (use apparent|allocated)", opts.SizeBasis)
	}

	switch opts.Backend {
	case "":
		opts.Backend = BackendAuto
	case BackendAuto, BackendSerial, BackendParallel:
	default:
		return normalizedOptions{}, fmt.Errorf(
			"unsupported backend %q (use auto|serial|parallel)", opts.Backend)
	}
	if opts.Workers < 0 {
		return normalizedOptions{}, errors.New("workers cannot be negative")
	}
	if opts.Workers == 0 {
		opts.Workers = 4
	}
	if opts.Backend == BackendAuto {
		if opts.Workers == 1 {
			opts.Backend = BackendSerial
		} else {
			opts.Backend = BackendParallel
		}
	}
	if opts.Backend == BackendSerial {
		if opts.Workers != 1 && opts.Workers != 4 {
			return normalizedOptions{}, errors.New("serial backend requires --workers 1 or auto")
		}
		opts.Workers = 1
	}
	if opts.MaxOpenDirs < 0 {
		return normalizedOptions{}, errors.New("max open dirs cannot be negative")
	}
	if opts.MaxOpenDirs == 0 {
		opts.MaxOpenDirs = opts.Workers
	}
	if opts.MaxPendingDirs < 0 {
		return normalizedOptions{}, errors.New("max pending dirs cannot be negative")
	}
	if opts.MaxPendingDirs == 0 {
		opts.MaxPendingDirs = DefaultMaxPendingDirs
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if opts.Traversal != nil {
		if opts.EmissionMode != EmissionSummaryOnly {
			return normalizedOptions{}, errors.New("observation traversal requires summary-only emission")
		}
		if opts.Traversal.MaxDepth < -1 {
			return normalizedOptions{}, errors.New("traversal max depth cannot be less than -1")
		}
	}
	var roots []Root
	var admissionGaps map[string]Gap
	var err error
	if opts.Traversal != nil && opts.Traversal.DiscoveredRoots {
		roots, admissionGaps, err = admitDiscoveredRoots(opts.Roots)
	} else {
		roots, err = admitRoots(opts.Roots)
	}
	if err != nil {
		return normalizedOptions{}, err
	}
	if opts.RunID == "" {
		opts.RunID = fmt.Sprintf("inv-%d-%d", now.UnixNano(), os.Getpid())
	}
	exclusions, err := normalizeExclusions(opts.Exclusions)
	if err != nil {
		return normalizedOptions{}, err
	}
	stallTimeout := opts.StallTimeout
	if stallTimeout == 0 {
		stallTimeout = DefaultStallTimeout
	}
	alertAfter := opts.stallAlertAfter
	if alertAfter <= 0 {
		alertAfter = defaultStallAlertAfter
	}
	return normalizedOptions{
		Options: opts, roots: roots, exclusions: exclusions, now: now,
		stallTimeout: stallTimeout, alertAfter: alertAfter,
		admissionGaps: admissionGaps,
	}, nil
}

func normalizeExclusions(rawRules []string) ([]Exclusion, error) {
	out := make([]Exclusion, 0, len(rawRules))
	seen := make(map[Exclusion]struct{}, len(rawRules))
	for _, raw := range rawRules {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, errors.New("exclusion cannot be empty")
		}
		portable := strings.ReplaceAll(raw, "\\", "/")
		if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" || strings.HasPrefix(portable, "/") || hasWindowsVolumePrefix(portable) {
			return nil, fmt.Errorf("exclusion must be root-relative or a basename, not absolute: %q", raw)
		}
		rootRelative := strings.HasPrefix(portable, "./") || strings.Contains(portable, "/")
		parts := strings.Split(portable, "/")
		for _, part := range parts {
			if part == ".." {
				return nil, fmt.Errorf("exclusion cannot contain parent traversal: %q", raw)
			}
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(portable)))
		if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
			return nil, fmt.Errorf("invalid exclusion %q", raw)
		}
		rule := Exclusion{Kind: "basename", Value: clean}
		if rootRelative {
			rule.Kind = "root_relative"
		}
		if _, ok := seen[rule]; ok {
			continue
		}
		seen[rule] = struct{}{}
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

func hasWindowsVolumePrefix(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') ||
		(value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func excludedBy(rules []Exclusion, root Root, candidate string) bool {
	if len(rules) == 0 {
		return false
	}
	rel := relative(root, candidate)
	base := filepath.Base(candidate)
	for _, rule := range rules {
		switch rule.Kind {
		case "basename":
			if base == rule.Value {
				return true
			}
		case "root_relative":
			if rel == rule.Value {
				return true
			}
		}
	}
	return false
}

func admitRoots(requested []string) ([]Root, error) {
	paths := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		p, err := cleanRoot(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[p]; ok {
			continue
		}
		for _, existing := range paths {
			if pathContains(existing, p) || pathContains(p, existing) {
				return nil, fmt.Errorf(
					"overlapping roots are refused to prevent duplicate enumeration: %s and %s",
					existing, p)
			}
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	roots := make([]Root, 0, len(paths))
	for i, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("admit root %s: %w", p, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("inventory root is not a directory: %s", p)
		}
		meta := metadataOf(info)
		roots = append(roots, Root{
			ID:       "root-" + strconv.Itoa(i+1),
			Path:     p,
			DeviceID: meta.deviceID,
		})
	}
	return roots, nil
}

func cleanRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("inventory root cannot be empty")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		if raw == "~" {
			raw = home
		} else {
			raw = filepath.Join(home, raw[2:])
		}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", raw, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", abs, err)
	}
	return filepath.Clean(resolved), nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." {
		return rel == "."
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relative(root Root, path string) string {
	rel, err := filepath.Rel(root.Path, path)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func entryLess(a, b Entry, basis string) bool {
	as, _ := a.SelectedSize(basis)
	bs, _ := b.SelectedSize(basis)
	if as != bs {
		return as > bs
	}
	if a.RootID != b.RootID {
		return a.RootID < b.RootID
	}
	return a.RelativePath < b.RelativePath
}

func sortEntries(entries []Entry, basis string) {
	sort.Slice(entries, func(i, j int) bool {
		return entryLess(entries[i], entries[j], basis)
	})
}

// ParseAge accepts the command's compact age vocabulary.
func ParseAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, nil
	}
	if len(raw) < 2 {
		return 0, fmt.Errorf("invalid age %q", raw)
	}
	unit := raw[len(raw)-1]
	n, err := strconv.ParseInt(raw[:len(raw)-1], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid age %q (use Ns|Nm|Nh|Nd|Nw)", raw)
	}
	var factor time.Duration
	switch unit {
	case 's':
		factor = time.Second
	case 'm':
		factor = time.Minute
	case 'h':
		factor = time.Hour
	case 'd':
		factor = 24 * time.Hour
	case 'w':
		factor = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unsupported age unit %q (use s|m|h|d|w)", unit)
	}
	if n > int64((1<<63-1)/factor) {
		return 0, fmt.Errorf("age %q overflows duration", raw)
	}
	return time.Duration(n) * factor, nil
}

// ParseSize accepts integer binary-size forms such as 10M, 5GiB, and 1TB.
func ParseSize(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	if raw == "" {
		return 0, nil
	}
	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	value, err := strconv.ParseInt(raw[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", raw, err)
	}
	unit := raw[i:]
	multipliers := map[string]int64{
		"": 1, "B": 1,
		"K": 1 << 10, "KB": 1 << 10, "KIB": 1 << 10,
		"M": 1 << 20, "MB": 1 << 20, "MIB": 1 << 20,
		"G": 1 << 30, "GB": 1 << 30, "GIB": 1 << 30,
		"T": 1 << 40, "TB": 1 << 40, "TIB": 1 << 40,
		"P": 1 << 50, "PB": 1 << 50, "PIB": 1 << 50,
	}
	multiplier, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf(
			"unsupported size unit %q (use B|K|M|G|T|P, optionally iB)", unit)
	}
	if value > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("size %q overflows int64", raw)
	}
	return value * multiplier, nil
}

// fileMetadataOf is the per-file metadata source for accounting. Tests replace
// it to simulate a filesystem that reports no allocation.
var fileMetadataOf = metadataOf

type fileMetadata struct {
	allocated *int64
	deviceID  string
	fileID    string
}
