// Package bench provides the comparator harness for filesystem inventory
// measurement.
//
// The central rule of this package is that a measurement carries its own
// environment. Not alongside it, not in a README beside the results file, but
// in the same record as the number: filesystem type, OS and kernel, device
// class, cache state, CPU topology, worker count, and the corpus identifier.
//
// The rule is enforced by construction rather than by review. NewMeasurement
// refuses to build a row whose Environment is incomplete, so an unenvironmented
// result is not representable, let alone emittable. This is deliberately
// stricter than checking at review time: a benchmark whose environment was not
// recorded cannot be re-scoped afterwards, only re-run, and by then the machine
// that produced it may not exist.
//
// It also yields the extrapolation boundary for free. Whatever the harness has
// no row for is, by construction, not covered by runtime measurement, and any
// claim reaching further than the rows is extrapolation that must say so.
package bench

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// FieldSource records how an environment field was determined. The distinction
// matters: a filesystem type read from statfs and one typed in by an operator
// are not equally reliable, and a reader of the results is entitled to know
// which they are looking at.
type FieldSource string

const (
	// SourceMeasured means the harness read the value from the system.
	SourceMeasured FieldSource = "measured"

	// SourceDeclared means an operator asserted the value. Declared values
	// are still recorded; they are simply attributed.
	SourceDeclared FieldSource = "declared"
)

// DeviceClass describes the backing storage. Parallelism that helps on flash
// can hurt on a rotating disk, so a throughput number without this is not
// comparable to another machine's.
type DeviceClass string

const (
	DeviceSSD        DeviceClass = "ssd"
	DeviceRotational DeviceClass = "rotational"
	DeviceNetwork    DeviceClass = "network"

	// DeviceUnknown is a valid, emittable value: an operator stating that
	// the device class is not known is making a claim about coverage. It is
	// distinct from the field being absent, which is not emittable.
	DeviceUnknown DeviceClass = "unknown"
)

// CacheState records whether the page cache was warm for the measured tree.
// The same walk over the same corpus differs by an order of magnitude between
// the two, so a row without it cannot be compared to anything.
type CacheState string

const (
	CacheCold CacheState = "cold"
	CacheWarm CacheState = "warm"
)

// Environment is the full set of conditions a measurement was taken under.
// Every field is required; see Validate.
type Environment struct {
	// OS and Arch identify the platform.
	OS   string `json:"os"`
	Arch string `json:"arch"`

	// KernelVersion identifies the kernel build.
	KernelVersion string `json:"kernel_version"`

	// GoVersion identifies the toolchain, which affects the in-process
	// comparators but not the external ones.
	GoVersion string `json:"go_version"`

	// CPULogical is the logical processor count available to the process.
	CPULogical int `json:"cpu_logical"`

	// FilesystemType is the filesystem of the measured root, such as apfs,
	// ext4, xfs, or tmpfs.
	FilesystemType       string      `json:"filesystem_type"`
	FilesystemTypeSource FieldSource `json:"filesystem_type_source"`

	// DeviceClass describes the backing storage.
	DeviceClass       DeviceClass `json:"device_class"`
	DeviceClassSource FieldSource `json:"device_class_source"`

	// CacheState records page-cache warmth.
	CacheState       CacheState  `json:"cache_state"`
	CacheStateSource FieldSource `json:"cache_state_source"`

	// CachePurgeAttempted and CachePurgeSucceeded record whether a cold
	// state was actually produced rather than merely asserted. Dropping
	// caches requires privilege the harness does not assume, so a cold claim
	// the harness could not enforce is attributable.
	CachePurgeAttempted bool `json:"cache_purge_attempted"`
	CachePurgeSucceeded bool `json:"cache_purge_succeeded"`

	// CachePurgeDetail explains why a purge did not happen, so a reader of
	// an unenforced cold row learns whether it lacked privilege, lacked a
	// mechanism, or failed for some other reason.
	CachePurgeDetail string `json:"cache_purge_detail,omitempty"`

	// Workers is the concurrency the measured implementation ran with. One
	// for serial comparators and external tools that do not expose it.
	Workers int `json:"workers"`

	// CorpusID identifies the exact shape measured. Two rows carrying the
	// same value ran against the same corpus; two rows that do not are not
	// directly comparable, whatever else they share.
	CorpusID string `json:"corpus_id"`

	// CorpusCoverage is the corpus's own statement of what it does and does
	// not contain, carried alongside so a row states the limits of the tree
	// it measured as well as the machine.
	CorpusCoverage string `json:"corpus_coverage"`
}

// ErrIncompleteEnvironment is returned when a measurement is attempted without
// a complete environment.
var ErrIncompleteEnvironment = errors.New("bench: measurement environment is incomplete")

// Validate reports every missing or invalid environment field at once, so an
// operator fixes the whole set in one pass rather than one field per run.
func (e Environment) Validate() error {
	var missing []string

	if e.OS == "" {
		missing = append(missing, "os")
	}
	if e.Arch == "" {
		missing = append(missing, "arch")
	}
	if e.KernelVersion == "" {
		missing = append(missing, "kernel_version")
	}
	if e.GoVersion == "" {
		missing = append(missing, "go_version")
	}
	if e.CPULogical <= 0 {
		missing = append(missing, "cpu_logical")
	}
	if e.FilesystemType == "" {
		missing = append(missing, "filesystem_type")
	}
	if !validSource(e.FilesystemTypeSource) {
		missing = append(missing, "filesystem_type_source")
	}
	if !validDeviceClass(e.DeviceClass) {
		missing = append(missing, "device_class")
	}
	if !validSource(e.DeviceClassSource) {
		missing = append(missing, "device_class_source")
	}
	if e.CacheState != CacheCold && e.CacheState != CacheWarm {
		missing = append(missing, "cache_state")
	}
	if !validSource(e.CacheStateSource) {
		missing = append(missing, "cache_state_source")
	}
	if e.Workers <= 0 {
		missing = append(missing, "workers")
	}
	if e.CorpusID == "" {
		missing = append(missing, "corpus_id")
	}
	if e.CorpusCoverage == "" {
		missing = append(missing, "corpus_coverage")
	}

	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: %s", ErrIncompleteEnvironment, strings.Join(missing, ", "))
}

// Summary renders the environment as a single comparable line.
func (e Environment) Summary() string {
	purge := ""
	if e.CacheState == CacheCold && e.CachePurgeAttempted && !e.CachePurgeSucceeded {
		purge = " (purge failed"
		if e.CachePurgeDetail != "" {
			purge += ": " + e.CachePurgeDetail
		}
		purge += ")"
	}
	return fmt.Sprintf("%s/%s %s · %s · %s · %s cache%s · %d workers · %d cpu · corpus %s",
		e.OS, e.Arch, e.KernelVersion, e.FilesystemType, e.DeviceClass,
		e.CacheState, purge, e.Workers, e.CPULogical, e.CorpusID)
}

// Comparable reports whether two environments differ only in the dimension
// under test, and names the fields that differ.
//
// A worker sweep varies Workers and holds everything else fixed; a warm/cold
// comparison varies CacheState. Rows differing in more than the swept
// dimension are not a controlled comparison, and reporting them as one is the
// most common way a benchmark overstates what it showed.
func (e Environment) Comparable(other Environment, swept ...string) (bool, []string) {
	sweptSet := map[string]bool{}
	for _, s := range swept {
		sweptSet[s] = true
	}

	differing := map[string]bool{}
	check := func(name string, same bool) {
		if !same && !sweptSet[name] {
			differing[name] = true
		}
	}

	check("os", e.OS == other.OS)
	check("arch", e.Arch == other.Arch)
	check("kernel_version", e.KernelVersion == other.KernelVersion)
	check("cpu_logical", e.CPULogical == other.CPULogical)
	check("filesystem_type", e.FilesystemType == other.FilesystemType)
	check("device_class", e.DeviceClass == other.DeviceClass)
	check("cache_state", e.CacheState == other.CacheState)
	check("workers", e.Workers == other.Workers)
	check("corpus_id", e.CorpusID == other.CorpusID)

	if len(differing) == 0 {
		return true, nil
	}
	names := make([]string, 0, len(differing))
	for n := range differing {
		names = append(names, n)
	}
	sort.Strings(names)
	return false, names
}

// EnvOptions carries the values the harness cannot measure and an operator
// must declare.
type EnvOptions struct {
	// DeviceClass is required. Pass DeviceUnknown to state explicitly that
	// it is not known; leaving it empty is an absent field, not an unknown
	// one, and will fail validation.
	DeviceClass DeviceClass

	// CacheState is required.
	CacheState CacheState

	// CachePurgeAttempted and CachePurgeSucceeded describe whether a cold
	// state was enforced or only asserted, and CachePurgeDetail says why
	// when it was not.
	CachePurgeAttempted bool
	CachePurgeSucceeded bool
	CachePurgeDetail    string

	// Workers is the concurrency of the measured implementation.
	Workers int

	// CorpusID and CorpusCoverage come from the corpus manifest.
	CorpusID       string
	CorpusCoverage string
}

// CaptureEnvironment reads what the system can tell it about path and merges
// the operator-declared values from opts.
//
// It does not fail on an incomplete result; it returns what it found so the
// caller can see which fields still need declaring. Validate is the gate.
func CaptureEnvironment(path string, opts EnvOptions) Environment {
	env := Environment{
		OS:                  runtime.GOOS,
		Arch:                runtime.GOARCH,
		GoVersion:           runtime.Version(),
		CPULogical:          runtime.NumCPU(),
		KernelVersion:       kernelVersion(),
		DeviceClass:         opts.DeviceClass,
		DeviceClassSource:   SourceDeclared,
		CacheState:          opts.CacheState,
		CacheStateSource:    SourceDeclared,
		CachePurgeAttempted: opts.CachePurgeAttempted,
		CachePurgeSucceeded: opts.CachePurgeSucceeded,
		CachePurgeDetail:    opts.CachePurgeDetail,
		Workers:             opts.Workers,
		CorpusID:            opts.CorpusID,
		CorpusCoverage:      opts.CorpusCoverage,
	}

	if fsType, ok := filesystemType(path); ok {
		env.FilesystemType = fsType
		env.FilesystemTypeSource = SourceMeasured
	}

	// A measured device class overrides nothing: an operator who declares
	// one is asserting knowledge the harness does not have. Only fill in a
	// measured value when none was declared.
	if env.DeviceClass == "" {
		if class, ok := deviceClass(path); ok {
			env.DeviceClass = class
			env.DeviceClassSource = SourceMeasured
		}
	}

	return env
}

func validSource(s FieldSource) bool {
	return s == SourceMeasured || s == SourceDeclared
}

func validDeviceClass(c DeviceClass) bool {
	switch c {
	case DeviceSSD, DeviceRotational, DeviceNetwork, DeviceUnknown:
		return true
	default:
		return false
	}
}
