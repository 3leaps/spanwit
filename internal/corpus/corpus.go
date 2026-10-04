// Package corpus builds deterministic filesystem fixture corpora for inventory
// benchmarking and coverage-honesty testing.
//
// Two properties make this package useful as an oracle:
//
//  1. The manifest is derived from the construction plan, never from a walk of
//     the built tree. Counting the result with a walker and then validating a
//     walker against those counts would be circular; every number a Manifest
//     reports is known before the corresponding object is created.
//
//  2. A shape that cannot be created on the current platform is recorded as an
//     Omission rather than silently skipped. A corpus that quietly built fewer
//     adversarial shapes than requested would report the same success as one
//     that built them all, and a benchmark run on it would overstate its own
//     coverage.
//
// Corpora are reproducible: given the same Spec, the same tree is built, with
// deterministic content, sizes, and modification times. No wall clock is read
// during construction.
package corpus

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// Kind identifies a corpus shape. Adversarial kinds stress traversal
// correctness; synthetic kinds stress traversal throughput at a known shape.
type Kind string

const (
	// KindMixedPermissions contains directories and files the building user
	// cannot read, interleaved with readable siblings.
	KindMixedPermissions Kind = "mixed-permissions"

	// KindSymlinks contains file symlinks, directory symlinks, dangling
	// symlinks, and a symlink cycle.
	KindSymlinks Kind = "symlinks"

	// KindSparseFiles contains files whose apparent size greatly exceeds
	// their allocated size.
	KindSparseFiles Kind = "sparse-files"

	// KindHardLinks contains multiple directory entries sharing one inode.
	KindHardLinks Kind = "hard-links"

	// KindWide contains few directories each holding many entries.
	KindWide Kind = "wide"

	// KindDeep contains a single deeply nested directory chain.
	KindDeep Kind = "deep"

	// KindManyEmptyDirs contains a large population of empty directories.
	KindManyEmptyDirs Kind = "many-empty-dirs"

	// KindDevTree approximates a development tree: many small files in
	// build- and cache-shaped directories.
	KindDevTree Kind = "dev-tree"

	// KindLargeFiles approximates a media or VM-image tree: few files, each
	// large.
	KindLargeFiles Kind = "large-files"
)

// AllKinds lists every buildable corpus kind in a stable order.
func AllKinds() []Kind {
	return []Kind{
		KindMixedPermissions,
		KindSymlinks,
		KindSparseFiles,
		KindHardLinks,
		KindWide,
		KindDeep,
		KindManyEmptyDirs,
		KindDevTree,
		KindLargeFiles,
	}
}

// FixtureEpoch is the base modification time for corpus objects. It is a fixed
// point rather than a wall-clock read so that age-window filters have a stable
// oracle. Chosen well in the past so every fixture object is unambiguously
// older than any test's captured clock.
var FixtureEpoch = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

// Spec describes a corpus to build. Scale is interpreted per Kind; see the
// named constructors for what it controls in each shape.
type Spec struct {
	// Kind selects the shape. Required.
	Kind Kind

	// Scale is the shape's magnitude. Required, must be positive. Tests use
	// small values; benchmark corpora use large ones. The manifest is exact
	// at every scale.
	Scale int

	// Seed makes content and size jitter reproducible.
	Seed int64

	// FileSize is the nominal per-file size in bytes for shapes that write
	// file content. Zero selects the shape's default.
	FileSize int64

	// BaseTime is the modification time of the first object created; later
	// objects are spaced deterministically before it. Zero selects
	// FixtureEpoch.
	BaseTime time.Time

	// RecordEntries populates Manifest.Entries with one record per created
	// object. Intended for correctness tests at small scale; at benchmark
	// scale it costs memory proportional to the corpus.
	RecordEntries bool
}

// ObjectClass classifies a created filesystem object.
type ObjectClass string

const (
	ClassDir     ObjectClass = "dir"
	ClassFile    ObjectClass = "file"
	ClassSymlink ObjectClass = "symlink"
)

// Entry is one object in the construction plan, recorded before creation.
// RelPath is slash-separated and relative to the corpus root.
type Entry struct {
	RelPath string
	Class   ObjectClass

	// ApparentBytes is the logical file size. Zero for directories and
	// symlinks.
	ApparentBytes int64

	// Sparse marks a file whose allocated size is intended to be far below
	// its apparent size.
	Sparse bool

	// LinkGroup is non-empty when this entry shares an inode with other
	// entries carrying the same value.
	LinkGroup string

	// LinkTarget is the raw target for symlinks, slash-separated.
	LinkTarget string

	// Dangling marks a symlink whose target intentionally does not exist.
	Dangling bool

	// Unreadable marks an object the building user cannot open or list.
	Unreadable bool

	// ModTime is the modification time set on the object.
	ModTime time.Time
}

// HardLinkGroup records a set of paths sharing one inode, and the apparent
// size counted once for that inode.
type HardLinkGroup struct {
	Name          string
	Paths         []string
	ApparentBytes int64
}

// Omission records a shape element that was requested but could not be created
// on this platform or under this user, with the reason.
//
// This is the fixture-level form of the rule the benchmark harness applies to
// measurements: a result that cannot state its own coverage is not emittable.
type Omission struct {
	Element string
	Reason  string
}

// Manifest is the ground truth for a built corpus, derived entirely from the
// construction plan.
type Manifest struct {
	// ID identifies the corpus for cross-referencing benchmark rows. It
	// encodes kind, scale, and seed, so two rows carrying the same ID were
	// measured against the same shape.
	ID string

	Kind Kind
	Spec Spec

	// Root is the absolute path of the built corpus root.
	Root string

	// Dirs counts directories, excluding the corpus root itself.
	Dirs int64

	// Files counts regular files, counting each hard-linked directory entry
	// separately. See UniqueFiles for the inode-distinct count.
	Files int64

	// Symlinks counts symbolic links of every kind.
	Symlinks int64

	// ApparentBytes sums the logical size of every regular file entry,
	// counting hard-linked entries once per directory entry.
	//
	// This is deliberately not a reclaimable-space figure: hard links and
	// sparse files both break that equivalence. Use UniqueApparentBytes for
	// the inode-distinct sum, and expect neither to equal allocated blocks.
	ApparentBytes int64

	// Unreadable lists relative paths the building user cannot read.
	Unreadable []string

	// UnreachableObjects counts objects that exist and are included in the
	// counts above but lie behind an unreadable directory, so no traversal
	// can observe them.
	//
	// This is the oracle for the coverage-honesty tests: a walker that
	// reports TotalObjects entries on this corpus has miscounted, and one
	// that reports TotalObjects-UnreachableObjects without also reporting a
	// gap has presented a projection as the whole scope.
	UnreachableObjects int64

	// HardLinkGroups lists inode-sharing sets.
	HardLinkGroups []HardLinkGroup

	// Omitted lists requested elements that could not be built.
	Omitted []Omission

	// Capabilities records what the platform supported at build time.
	Capabilities Capabilities

	// Entries is populated only when Spec.RecordEntries was set.
	Entries []Entry
}

// UniqueFiles returns the number of distinct file inodes: every non-linked
// file, plus one per hard-link group.
func (m *Manifest) UniqueFiles() int64 {
	linked := int64(0)
	for _, g := range m.HardLinkGroups {
		linked += int64(len(g.Paths))
	}
	return m.Files - linked + int64(len(m.HardLinkGroups))
}

// UniqueApparentBytes returns the apparent byte sum counting each hard-linked
// inode once. It remains an apparent figure: sparse files still report more
// than they allocate.
func (m *Manifest) UniqueApparentBytes() int64 {
	total := m.ApparentBytes
	for _, g := range m.HardLinkGroups {
		// The group's size was added once per directory entry; keep one.
		total -= g.ApparentBytes * int64(len(g.Paths)-1)
	}
	return total
}

// TotalObjects returns every object below the root: directories, files, and
// symlinks, including objects no traversal can reach.
func (m *Manifest) TotalObjects() int64 {
	return m.Dirs + m.Files + m.Symlinks
}

// ObservableObjects returns the objects a traversal running as the building
// user can actually encounter: every object except those sealed behind an
// unreadable directory.
//
// A complete traversal of this corpus should report this count and a gap
// covering the difference from TotalObjects. Reporting this count alone, with
// no gap, is the coverage-honesty failure these fixtures exist to detect.
func (m *Manifest) ObservableObjects() int64 {
	return m.TotalObjects() - m.UnreachableObjects
}

// Complete reports whether every requested shape element was built. A corpus
// with omissions is still usable, but any claim made from it is bounded by
// what it could not contain.
func (m *Manifest) Complete() bool {
	return len(m.Omitted) == 0
}

// CoverageNote renders a one-line statement of what the corpus does and does
// not contain, suitable for embedding beside a measurement.
func (m *Manifest) CoverageNote() string {
	if m.Complete() {
		return fmt.Sprintf("%s: all requested shape elements present", m.ID)
	}
	parts := make([]string, 0, len(m.Omitted))
	for _, o := range m.Omitted {
		parts = append(parts, fmt.Sprintf("%s (%s)", o.Element, o.Reason))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s: omitted %d element(s): %v", m.ID, len(m.Omitted), parts)
}

// Path returns the absolute path of a manifest-relative entry path.
func (m *Manifest) Path(relPath string) string {
	return filepath.Join(m.Root, filepath.FromSlash(relPath))
}

// Validate reports whether the spec is buildable as written.
func (s Spec) Validate() error {
	if s.Kind == "" {
		return fmt.Errorf("corpus: kind is required")
	}
	known := false
	for _, k := range AllKinds() {
		if k == s.Kind {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("corpus: unknown kind %q", s.Kind)
	}
	if s.Scale <= 0 {
		return fmt.Errorf("corpus: scale must be positive, got %d", s.Scale)
	}
	if s.FileSize < 0 {
		return fmt.Errorf("corpus: file size must not be negative, got %d", s.FileSize)
	}
	return nil
}

// ID returns the stable identifier for a spec: the value benchmark rows carry
// to prove two measurements ran against the same shape.
func (s Spec) ID() string {
	return fmt.Sprintf("%s/scale=%d/seed=%d/size=%d", s.Kind, s.Scale, s.Seed, s.fileSize())
}

func (s Spec) fileSize() int64 {
	if s.FileSize > 0 {
		return s.FileSize
	}
	return defaultFileSize(s.Kind)
}

func (s Spec) baseTime() time.Time {
	if s.BaseTime.IsZero() {
		return FixtureEpoch
	}
	return s.BaseTime
}

// defaultFileSize returns the nominal per-file size for a kind when the spec
// does not set one.
func defaultFileSize(k Kind) int64 {
	switch k {
	case KindLargeFiles:
		return 64 << 20 // 64 MiB
	case KindSparseFiles:
		return 1 << 30 // 1 GiB apparent, near-zero allocated
	case KindDevTree:
		return 4 << 10 // 4 KiB, build-output shaped
	default:
		return 1 << 10 // 1 KiB
	}
}
