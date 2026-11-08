package space

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
)

// Guest probe and recipe assembly for container_runtime.

const guestProbeTimeout = 20 * time.Second

// ContainerRuntimeOptions controls collection. ProbeVM opts into running a
// command inside the engine VM (`colima ssh`); without it the guest filesystem
// is not_probed for VM engines.
type ContainerRuntimeOptions struct {
	ProbeVM       bool
	RefDisclosure string // aggregate (default) or local
	Env           EngineEnv
	// Seams for tests; nil uses the real socket, runner and statfs.
	API    func(socketPath string) EngineAPI
	Runner capacity.CommandRunner
	Statfs func(path string) (total, avail int64, err error)
}

// CollectContainerRuntime assembles the section. It never fails the report:
// every problem becomes a status and reason on the section.
func CollectContainerRuntime(ctx context.Context, opts ContainerRuntimeOptions) *ContainerRuntime {
	disclosure := opts.RefDisclosure
	if disclosure != RefDisclosureLocal {
		disclosure = RefDisclosureAggregate
	}
	rt := &ContainerRuntime{RefDisclosure: disclosure, Buckets: []RuntimeBucket{}, Recipe: []RuntimeRecipeStep{}}

	ep, err := ResolveEngineEndpoint(opts.Env)
	if err != nil {
		rt.Engine = EngineNone
		rt.Detection = EngineDetection{Status: DetectionUnavailable, Basis: []string{}, Reason: ReasonEndpointUnresolved}
		rt.GuestFilesystem = guestUnavailable(ProbeUnavailable, ReasonEndpointUnresolved)
		return rt
	}
	if ep.Remote || ep.SocketPath == "" || strings.HasPrefix(ep.SocketPath, "//./pipe/") {
		rt.Engine = EngineUnknown
		rt.Detection = EngineDetection{Status: DetectionUnidentified, Basis: []string{DetectionBasisEndpointPath}, Reason: ReasonUnsupportedEngine}
		rt.GuestFilesystem = guestUnavailable(ProbeNotProbed, ReasonUnsupportedEngine)
		return rt
	}

	newAPI := opts.API
	if newAPI == nil {
		newAPI = NewSocketEngineAPI
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*engineCallTimeout)
	defer cancel()
	// The VM's disk on the host is measured from the host side, so it is
	// reported whether or not the engine answers (a stopped VM still holds it).
	if ep.Engine == EngineColima {
		rt.HostDisk = colimaHostDisk(opts.Env.Home, ep.Profile)
	}
	snap, err := readEngineSnapshot(callCtx, newAPI(ep.SocketPath))
	if err != nil {
		reason, status := engineConnectReason(err, callCtx, ep)
		rt.Engine = EngineUnknown
		if ep.Engine != EngineUnknown {
			rt.Engine = ep.Engine
		}
		rt.Detection = EngineDetection{Status: DetectionUnidentified, Basis: []string{DetectionBasisEndpointPath}, Reason: reason, HTTPStatus: status}
		if rt.Engine == EngineNative {
			// native without corroboration is not trusted for host statfs
			rt.Engine = EngineUnknown
		}
		rt.GuestFilesystem = guestUnavailable(ProbeUnavailable, reason)
		return rt
	}

	rt.APIVersion = normalizeAPIVersion(snap.APIVersion)
	rt.EngineArch = normalizeArch(snap.Info.Architecture)
	engine, ok := CorroborateEngine(ep, snap.Info, opts.Env.Hostname)
	rt.Engine = engine
	if ok {
		rt.Detection = EngineDetection{Status: DetectionIdentified, Basis: []string{DetectionBasisEndpointPath, DetectionBasisEngineInfo}}
	} else {
		reason := ReasonUnsupportedEngine
		if ep.Engine != EngineUnknown {
			reason = ReasonEngineDisagreement
		}
		rt.Detection = EngineDetection{Status: DetectionUnidentified, Basis: []string{DetectionBasisEndpointPath, DetectionBasisEngineInfo}, Reason: reason}
	}

	buckets, refs := buildRuntimeBuckets(snap, rt.EngineArch)
	rt.Buckets = buckets
	families, familyRefs := buildImageFamilies(snap, rt.EngineArch, dfImageFacts(snap.DF))
	rt.ImageFamilies = &families
	refs = capRefs(append(refs, familyRefs...))
	if disclosure == RefDisclosureLocal && len(refs) > 0 {
		rt.ImageRefs = refs
	}

	rt.GuestFilesystem = probeGuest(ctx, rt.Engine, ep, snap.Info.DockerRootDir, opts)
	rt.Recipe = buildRuntimeRecipe(rt, unusedImageUnique(snap.DF), stoppedContainers(snap), opts.ProbeVM)
	return rt
}

// engineConnectReason classifies a failed /version or /info call. A non-200
// answer is engine_error (reached, but nothing to corroborate). A refused
// connection is engine_stopped only when the endpoint already names a known
// local engine, and so is a missing socket except for a native daemon; on an
// unknown endpoint the same errors
// stay engine_unreachable, and so do permission errors and resets, for which
// "start the VM" would be wrong advice.
func engineConnectReason(err error, callCtx context.Context, ep EngineEndpoint) (string, *int) {
	var se *engineStatusError
	switch {
	case errors.Is(err, errEngineParse), errors.Is(err, errEngineBodyTooLarge):
		return ReasonParseFailed, nil
	case errors.As(err, &se):
		return ReasonEngineError, httpStatusOf(err)
	case errors.Is(callCtx.Err(), context.DeadlineExceeded), isTimeout(err):
		return ReasonTimeout, nil
	case ep.Engine != EngineUnknown && errors.Is(err, syscall.ECONNREFUSED):
		return ReasonEngineStopped, nil
	case ep.Engine != EngineUnknown && ep.Engine != EngineNative && errors.Is(err, syscall.ENOENT):
		// A VM or app engine removes its socket when it is down. A missing
		// native socket is as likely "not installed" as "not running".
		return ReasonEngineStopped, nil
	}
	return ReasonEngineUnreachable, nil
}

// colimaHostDisk measures the colima VM's disk files on the host: logical
// size and the bytes actually allocated (the disk is sparse). Nil when the
// disk directory does not exist; profile and paths never reach the report.
func colimaHostDisk(home, profile string) *RuntimeHostDisk {
	if home == "" || profile == "" || profile == "." || profile == ".." {
		return nil
	}
	instance := "colima"
	if profile != "default" {
		instance = "colima-" + profile
	}
	dir := filepath.Join(home, ".colima", "_lima", "_disks", instance)
	files, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &RuntimeHostDisk{Status: ProbeUnavailable, Reason: ReasonProbeFailed}
	}
	var n, logicalTotal, allocatedTotal int64
	for _, f := range files {
		if !f.Type().IsRegular() {
			continue
		}
		info, err := f.Info()
		if err != nil {
			return &RuntimeHostDisk{Status: ProbeUnavailable, Reason: ReasonProbeFailed}
		}
		logical, allocated, ok := fileAllocation(info)
		if !ok {
			return &RuntimeHostDisk{Status: ProbeUnavailable, Reason: ReasonUnsupportedEngine}
		}
		if logical == 0 {
			continue
		}
		n++
		logicalTotal += logical
		allocatedTotal += allocated
	}
	if n == 0 {
		return &RuntimeHostDisk{Status: ProbeUnavailable, Reason: ReasonProbeFailed}
	}
	return &RuntimeHostDisk{Status: ProbeMeasured, Files: i64p(n),
		LogicalBytes: i64p(logicalTotal), AllocatedBytes: i64p(allocatedTotal)}
}

// ContainerRuntimeFallback replaces a section that failed its own contract
// check, so a container-side defect never costs the operator the disk report.
func ContainerRuntimeFallback(disclosure string) *ContainerRuntime {
	if disclosure != RefDisclosureLocal {
		disclosure = RefDisclosureAggregate
	}
	return &ContainerRuntime{
		Engine:          EngineUnknown,
		Detection:       EngineDetection{Status: DetectionUnavailable, Basis: []string{}, Reason: ReasonParseFailed},
		RefDisclosure:   disclosure,
		GuestFilesystem: guestUnavailable(ProbeUnavailable, ReasonParseFailed),
		Buckets:         []RuntimeBucket{},
		Recipe:          []RuntimeRecipeStep{},
	}
}

func guestUnavailable(status, reason string) GuestFilesystem {
	return GuestFilesystem{Status: status, Basis: GuestBasisNone, Reason: reason}
}

func probeGuest(ctx context.Context, engine string, ep EngineEndpoint, dataRoot string, opts ContainerRuntimeOptions) GuestFilesystem {
	if !safeDataRoot(dataRoot) {
		return guestUnavailable(ProbeUnavailable, ReasonParseFailed)
	}
	switch engine {
	case EngineNative:
		statfs := opts.Statfs
		if statfs == nil {
			statfs = hostStatfs
		}
		total, avail, err := statfs(dataRoot)
		if err != nil {
			return guestUnavailable(ProbeUnavailable, ReasonProbeFailed)
		}
		return measuredGuest(GuestBasisHostStatfs, total, total-avail, avail)
	case EngineColima:
		if !opts.ProbeVM {
			return guestUnavailable(ProbeNotProbed, ReasonProbeNotRequested)
		}
		runner := opts.Runner
		if runner == nil {
			runner = capacity.ExecRunner{MaxOutput: 64 << 10}
		}
		pctx, cancel := context.WithTimeout(ctx, guestProbeTimeout)
		defer cancel()
		// df -P -B1: POSIX single-line records, exact bytes. The profile is an
		// argv value only; it is never written to the report.
		out, err := runner.Run(pctx, "colima", "ssh", "-p", ep.Profile, "--", "df", "-P", "-B1", dataRoot)
		if err != nil {
			var re *capacity.RunError
			if errors.As(err, &re) && re.Code == "timeout" {
				return guestUnavailable(ProbeUnavailable, ReasonTimeout)
			}
			return guestUnavailable(ProbeUnavailable, ReasonProbeFailed)
		}
		total, used, avail, ok := parseDFBytes(out)
		if !ok {
			return guestUnavailable(ProbeUnavailable, ReasonParseFailed)
		}
		return measuredGuest(GuestBasisGuestStatfs, total, used, avail)
	default:
		return guestUnavailable(ProbeNotProbed, ReasonUnsupportedEngine)
	}
}

// safeDataRootPattern admits a plain absolute path. colima joins the remote
// command for the guest shell, so the engine-reported data root must carry no
// shell metacharacters, whitespace or option-looking segments.
var safeDataRootPattern = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+/?$`)

func safeDataRoot(p string) bool {
	if len(p) > 256 || !safeDataRootPattern.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "." || seg == ".." || strings.HasPrefix(seg, "-") {
			return false
		}
	}
	return true
}

func measuredGuest(basis string, total, used, avail int64) GuestFilesystem {
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return GuestFilesystem{Status: ProbeMeasured, Basis: basis,
		TotalBytes: i64p(total), UsedBytes: i64p(used), AvailBytes: i64p(avail),
		Level: pressureLevel(avail, pct)}
}

// parseDFBytes reads `df -P -B1` output: a header line and one record whose
// columns 2-4 are total, used and available bytes.
func parseDFBytes(out []byte) (total, used, avail int64, ok bool) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Filesystem") {
		return 0, 0, 0, false
	}
	f := strings.Fields(lines[1])
	if len(f) < 6 {
		return 0, 0, 0, false
	}
	vals := make([]int64, 3)
	for i := range vals {
		n, err := strconv.ParseInt(f[i+1], 10, 64)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		vals[i] = n
	}
	return vals[0], vals[1], vals[2], vals[0] > 0
}

func hostStatfs(path string) (int64, int64, error) {
	sample, err := capacity.SampleFilesystem(path)
	if err != nil {
		return 0, 0, err
	}
	return sample.Total, sample.Available, nil
}

func normalizeAPIVersion(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts[:2] {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	return parts[0] + "." + parts[1]
}

// buildRuntimeRecipe orders relief by cost class (none, rebuild, re_pull,
// data_loss_risk), then trims the guest so the host gets the space back, then
// advises growth when the guest stays full. Commands are generic and never
// name an image, volume, profile or path; placeholders stand in for those.
func buildRuntimeRecipe(rt *ContainerRuntime, unusedImageBytes *int64, stopped stoppedSet, probedVM bool) []RuntimeRecipeStep {
	b := map[string]RuntimeBucket{}
	for _, x := range rt.Buckets {
		b[x.Kind] = x
	}
	est := func(kind, bound string, pick func(RuntimeBucket) *int64) RuntimeMeasure {
		x, ok := b[kind]
		if !ok {
			return RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonProbeFailed}
		}
		if x.Status != ProbeMeasured || pick(x) == nil {
			reason := x.Reason
			if reason == "" {
				reason = ReasonParseFailed
			}
			return RuntimeMeasure{Status: ProbeUnavailable, Reason: reason}
		}
		return RuntimeMeasure{Status: ProbeMeasured, Bytes: pick(x), Bound: bound}
	}
	nonZero := func(m RuntimeMeasure) bool { return m.Status != ProbeMeasured || *m.Bytes > 0 }
	unique := func(x RuntimeBucket) *int64 { return x.UniqueBytes }
	bytesOf := func(x RuntimeBucket) *int64 { return x.Bytes }

	var steps []RuntimeRecipeStep
	add := func(s RuntimeRecipeStep) {
		if nonZero(s.Estimate) {
			steps = append(steps, s)
		}
	}
	add(RuntimeRecipeStep{Step: StepDanglingImagePrune, CostClass: CostNone,
		Estimate: est(BucketDanglingImages, BoundLower, unique), Command: []string{"docker", "image", "prune"}})
	if stopped.present() {
		steps = append(steps, RuntimeRecipeStep{Step: StepContainerPrune, CostClass: CostNone,
			Estimate: stopped.bytes, Command: []string{"docker", "container", "prune"},
			Warning: "Removes every stopped container; review `docker ps -a` first. Images used only by stopped containers become removable afterwards."})
	}
	add(RuntimeRecipeStep{Step: StepBuildCachePrune, CostClass: CostRebuild,
		Estimate: est(BucketBuildCache, BoundExact, func(x RuntimeBucket) *int64 { return x.ReclaimableBytes }),
		Command:  []string{"docker", "builder", "prune"}})
	add(RuntimeRecipeStep{Step: StepForeignPlatformRemove, CostClass: CostRePull,
		Estimate: est(BucketForeignPlatform, BoundUpper, bytesOf),
		Command:  []string{"docker", "image", "rm", "--force", "--platform", "<os/arch>", "<image>"},
		Warning:  "Removes that platform from every tag sharing the content; --force is required when content is shared."})
	if f := rt.ImageFamilies; f != nil {
		cost := CostRePull
		if f.LocallyBuilt != nil && *f.LocallyBuilt > 0 {
			cost = CostRebuild
		}
		add(RuntimeRecipeStep{Step: StepOlderSiblingRemove, CostClass: cost,
			Estimate: olderSiblingEstimate(*f), Command: []string{"docker", "image", "rm", "<image>"},
			Warning: "Older versions in the same version line (repository, variant and major version). Only containers are checked: compose files and Dockerfile FROM pins are not. Confirm before removing; --image-refs lists them for local review."})
	}
	// prune --all removes every unused image. With none in use it frees the
	// whole deduplicated layer total (exact); otherwise the unused images'
	// unique bytes omit layers shared only among them (lower bound).
	pruneAll := est(BucketImages, BoundLower, unique)
	if pruneAll.Status == ProbeMeasured {
		img := b[BucketImages]
		switch {
		case img.InUse != nil && *img.InUse == 0 && img.Bytes != nil:
			pruneAll.Bytes, pruneAll.Bound = img.Bytes, BoundExact
		case unusedImageBytes == nil:
			pruneAll = RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonParseFailed}
		default:
			pruneAll.Bytes = unusedImageBytes
		}
	}
	add(RuntimeRecipeStep{Step: StepImagePruneAll, CostClass: CostRePull,
		Estimate: pruneAll, Command: []string{"docker", "image", "prune", "--all"},
		Warning: "Removes every image not used by a container, including images CI and tests need; expect a full re-pull."})
	add(RuntimeRecipeStep{Step: StepVolumePrune, CostClass: CostDataLossRisk,
		Estimate: est(BucketVolumesUnreferenced, BoundUpper, bytesOf), Command: []string{"docker", "volume", "prune", "--all"},
		Warning: "Unreferenced volumes can still hold data (databases, caches); review before pruning."})

	if rt.Engine == EngineColima {
		trimReason := ReasonProbeNotRequested
		if probedVM {
			trimReason = ReasonUnsupportedEngine
		}
		trim, trimWarning := trimEstimate(rt, trimReason)
		steps = append(steps, RuntimeRecipeStep{Step: StepGuestTrim, CostClass: CostNone,
			Estimate: trim, Warning: trimWarning,
			Command: []string{"colima", "ssh", "-p", "<profile>", "--", "sudo", "fstrim", "-v", "<data-root>"}})
		g := rt.GuestFilesystem
		if g.Status == ProbeMeasured && g.Level != PressureOK {
			steps = append(steps, RuntimeRecipeStep{Step: StepDiskGrow, CostClass: CostNone,
				Estimate: RuntimeMeasure{Status: ProbeNotProbed, Reason: trimReason},
				Command:  []string{"colima", "stop", "-p", "<profile>"},
				Warning:  "Then `colima start -p <profile> --disk <GiB>`. Colima disks grow only; they cannot shrink."})
		}
	}
	return steps
}

// trimGapLow is the trim estimate below which trimming now returns little
// and space must first be freed inside the VM.
const trimGapLow = 1 << 30

// trimEstimate is the trim gap: the VM disk's host allocation minus the
// engine filesystem's used bytes (df Used: total minus free), clamped at
// zero. It is an upper bound and an estimate: the allocation covers the
// whole disk, the guest figure one filesystem. Both sides must be measured.
func trimEstimate(rt *ContainerRuntime, notProbed string) (RuntimeMeasure, string) {
	g, h := rt.GuestFilesystem, rt.HostDisk
	switch {
	case g.Status != ProbeMeasured || g.UsedBytes == nil:
		if g.Status == ProbeNotProbed || g.Reason == "" {
			return RuntimeMeasure{Status: ProbeNotProbed, Reason: notProbed}, ""
		}
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: g.Reason}, ""
	case h == nil:
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonUnsupportedEngine}, ""
	case h.Status != ProbeMeasured || h.AllocatedBytes == nil:
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: h.Reason}, ""
	}
	gap := *h.AllocatedBytes - *g.UsedBytes
	if gap < 0 {
		gap = 0
	}
	m := RuntimeMeasure{Status: ProbeMeasured, Bytes: i64p(gap), Bound: BoundUpper}
	if gap < trimGapLow {
		return m, "Trim would return little now: the host already holds about what the VM uses. Free space inside the VM first (steps above), then trim."
	}
	return m, "Estimate: the VM disk's host allocation minus the space the engine's filesystem uses."
}

// olderSiblingEstimate is measured only when families and in-use are both
// known; otherwise no image is ranked and the reason says why.
func olderSiblingEstimate(f ImageFamilies) RuntimeMeasure {
	switch {
	case f.Status != ProbeMeasured:
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: f.Reason}
	case f.InUseStatus != ProbeMeasured:
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: f.InUseReason}
	case f.OlderSiblingBytes == nil:
		return RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonParseFailed}
	}
	return RuntimeMeasure{Status: ProbeMeasured, Bytes: f.OlderSiblingBytes, Bound: f.BytesBound}
}

// stoppedSet is what the recipe knows about stopped containers: a count from
// the container list and their writable-layer bytes from /system/df.
type stoppedSet struct {
	count *int64
	bytes RuntimeMeasure
}

func (s stoppedSet) present() bool {
	if s.count != nil {
		return *s.count > 0
	}
	return s.bytes.Status == ProbeMeasured && *s.bytes.Bytes > 0
}

func stoppedContainers(snap engineSnapshot) stoppedSet {
	var s stoppedSet
	if snap.ContainersErr == nil {
		s.count = i64p(countExited(snap.Containers))
	}
	switch {
	case snap.DFErr != nil:
		reason, _ := engineCallReason(snap.DFErr)
		s.bytes = RuntimeMeasure{Status: ProbeUnavailable, Reason: reason}
	case snap.DF.Containers == nil:
		s.bytes = RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonParseFailed}
	default:
		var n int64
		for _, c := range *snap.DF.Containers {
			if runningStates[c.State] {
				continue
			}
			if c.SizeRw == nil || *c.SizeRw < 0 {
				s.bytes = RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonParseFailed}
				return s
			}
			n += *c.SizeRw
		}
		s.bytes = RuntimeMeasure{Status: ProbeMeasured, Bytes: i64p(n), Bound: BoundExact}
	}
	return s
}

// dfFacts is what /system/df says about each image: unique bytes, present
// only when the engine computed shared size (SharedSize -1 means it did not,
// and the image size is then no unique figure), and the engine's own count
// of containers using it.
type dfFacts struct {
	unique     map[string]int64
	containers map[string]int64
}

// dfImageFacts is empty when /system/df did not answer with valid images.
func dfImageFacts(df engineDF) dfFacts {
	images, ok := validDFImages(df.Images)
	if !ok {
		return dfFacts{}
	}
	f := dfFacts{unique: map[string]int64{}, containers: map[string]int64{}}
	for _, im := range images {
		if im.SharedSize >= 0 && im.SharedSize <= im.Size {
			f.unique[im.ID] = im.Size - im.SharedSize
		}
		f.containers[im.ID] = im.Containers
	}
	return f
}

// unusedImageUnique sums unique bytes of images no container uses: the
// honest estimate for `docker image prune --all`. Nil when images are not
// measurable.
func unusedImageUnique(df engineDF) *int64 {
	images, ok := validDFImages(df.Images)
	if !ok {
		return nil
	}
	var n int64
	for _, im := range images {
		if im.Containers == 0 {
			n += imageUnique(im)
		}
	}
	return &n
}
