package space

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
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
	snap, err := readEngineSnapshot(callCtx, newAPI(ep.SocketPath))
	if err != nil {
		reason := ReasonEngineUnreachable
		switch {
		case errors.Is(err, errEngineParse), errors.Is(err, errEngineBodyTooLarge):
			reason = ReasonParseFailed
		case errors.Is(callCtx.Err(), context.DeadlineExceeded):
			reason = ReasonTimeout
		}
		rt.Engine = EngineUnknown
		if ep.Engine != EngineUnknown {
			rt.Engine = ep.Engine
		}
		rt.Detection = EngineDetection{Status: DetectionUnidentified, Basis: []string{DetectionBasisEndpointPath}, Reason: reason}
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
	if disclosure == RefDisclosureLocal && len(refs) > 0 {
		rt.ImageRefs = refs
	}

	rt.GuestFilesystem = probeGuest(ctx, rt.Engine, ep, snap.Info.DockerRootDir, opts)
	rt.Recipe = buildRuntimeRecipe(rt, unusedImageUnique(snap.DF), opts.ProbeVM)
	return rt
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
func buildRuntimeRecipe(rt *ContainerRuntime, unusedImageBytes *int64, probedVM bool) []RuntimeRecipeStep {
	b := map[string]RuntimeBucket{}
	for _, x := range rt.Buckets {
		b[x.Kind] = x
	}
	est := func(kind string, pick func(RuntimeBucket) *int64) RuntimeMeasure {
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
		return RuntimeMeasure{Status: ProbeMeasured, Bytes: pick(x)}
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
		Estimate: est(BucketDanglingImages, unique), Command: []string{"docker", "image", "prune"}})
	add(RuntimeRecipeStep{Step: StepBuildCachePrune, CostClass: CostRebuild,
		Estimate: est(BucketBuildCache, func(x RuntimeBucket) *int64 { return x.ReclaimableBytes }),
		Command:  []string{"docker", "builder", "prune"}})
	add(RuntimeRecipeStep{Step: StepForeignPlatformRemove, CostClass: CostRePull,
		Estimate: est(BucketForeignPlatform, bytesOf),
		Command:  []string{"docker", "image", "rm", "--force", "--platform", "<os/arch>", "<image>"},
		Warning:  "Removes that platform from every tag sharing the content; --force is required when content is shared."})
	add(RuntimeRecipeStep{Step: StepSupersededTagRemove, CostClass: CostRePull,
		Estimate: est(BucketSupersededTags, unique), Command: []string{"docker", "image", "rm", "<image>"}})
	pruneAll := est(BucketImages, unique)
	if pruneAll.Status == ProbeMeasured {
		if unusedImageBytes == nil {
			pruneAll = RuntimeMeasure{Status: ProbeUnavailable, Reason: ReasonParseFailed}
		} else {
			pruneAll.Bytes = unusedImageBytes
		}
	}
	add(RuntimeRecipeStep{Step: StepImagePruneAll, CostClass: CostRePull,
		Estimate: pruneAll, Command: []string{"docker", "image", "prune", "--all"},
		Warning: "Removes every image not used by a container, including images CI and tests need; expect a full re-pull."})
	add(RuntimeRecipeStep{Step: StepVolumePrune, CostClass: CostDataLossRisk,
		Estimate: est(BucketVolumesUnreferenced, bytesOf), Command: []string{"docker", "volume", "prune", "--all"},
		Warning: "Unreferenced volumes can still hold data (databases, caches); review before pruning."})

	if rt.Engine == EngineColima {
		trimReason := ReasonProbeNotRequested
		if probedVM {
			// No trim-headroom estimate yet (guest used vs host allocation).
			trimReason = ReasonUnsupportedEngine
		}
		steps = append(steps, RuntimeRecipeStep{Step: StepGuestTrim, CostClass: CostNone,
			Estimate: RuntimeMeasure{Status: ProbeNotProbed, Reason: trimReason},
			Command:  []string{"colima", "ssh", "-p", "<profile>", "--", "sudo", "fstrim", "-v", "<data-root>"}})
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
