package space

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The collector reads the Engine API over the resolved local socket. The API
// returns exact integer bytes, unlike the CLI's rounded human sizes, and needs
// no docker CLI on PATH. Every call is read-only (GET) and time-bounded.

const (
	// engineAPIVersion is the baseline for version/info/df. Image manifests
	// (per-platform availability) appear only from v1.47; older engines omit
	// the field, which must read as unavailable, never as zero residue.
	engineAPIVersion          = "v1.44"
	engineManifestsAPIVersion = "v1.47"
	engineCallTimeout         = 15 * time.Second
	engineMaxBody             = 64 << 20
	maxLocalImageRefs         = 50
	engineUserAgent           = "spanwit"
	engineHTTPBaseHost        = "http://engine"
)

// EngineAPI is the read-only Engine API surface the collector needs. Tests
// fake it with recorded, scrubbed responses.
type EngineAPI interface {
	Get(ctx context.Context, path string, out any) error
}

type socketEngineAPI struct{ client *http.Client }

// NewSocketEngineAPI dials a unix socket. Remote and named-pipe endpoints are
// not dialed: their guest is not this host's concern in v0.2.2.
func NewSocketEngineAPI(socketPath string) EngineAPI {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &socketEngineAPI{client: &http.Client{Transport: tr, Timeout: engineCallTimeout}}
}

func (s *socketEngineAPI) Get(ctx context.Context, path string, out any) error {
	version := engineAPIVersion
	if strings.HasPrefix(path, "/images/json") {
		version = engineManifestsAPIVersion
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, engineHTTPBaseHost+"/"+version+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", engineUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("engine API %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, engineMaxBody+1))
	if err != nil {
		return err
	}
	if len(body) > engineMaxBody {
		return errEngineBodyTooLarge
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %v", errEngineParse, err)
	}
	return nil
}

var (
	errEngineParse        = errors.New("engine API parse failed")
	errEngineBodyTooLarge = errors.New("engine API body too large")
)

// Engine API response subsets.
type engineVersion struct {
	APIVersion string `json:"ApiVersion"`
}

// engineDF fields are pointers so an absent or null field (older or
// compatible engines) reads as unavailable rather than as zero.
type engineDF struct {
	LayersSize *int64            `json:"LayersSize"`
	Images     *[]engineDFImage  `json:"Images"`
	Volumes    *[]engineDFVolume `json:"Volumes"`
	BuildCache *[]engineDFCache  `json:"BuildCache"`
}

type engineDFImage struct {
	ID         string   `json:"Id"`
	RepoTags   []string `json:"RepoTags"`
	Size       int64    `json:"Size"`
	SharedSize int64    `json:"SharedSize"` // -1 when not computed
	Containers int64    `json:"Containers"`
}

type engineDFVolume struct {
	UsageData *struct {
		RefCount int64 `json:"RefCount"`
		Size     int64 `json:"Size"` // -1 when not computed
	} `json:"UsageData"`
}

type engineDFCache struct {
	Size   int64 `json:"Size"`
	InUse  bool  `json:"InUse"`
	Shared bool  `json:"Shared"`
}

type engineImage struct {
	ID        string           `json:"Id"`
	RepoTags  []string         `json:"RepoTags"`
	Manifests []engineManifest `json:"Manifests"`
}

type engineManifest struct {
	Available bool   `json:"Available"`
	Kind      string `json:"Kind"`
	Size      struct {
		Total int64 `json:"Total"`
	} `json:"Size"`
	ImageData *struct {
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		} `json:"Platform"`
	} `json:"ImageData"`
}

// engineSnapshot is everything read from the API for one run.
type engineSnapshot struct {
	APIVersion string
	Info       EngineInfo
	DF         engineDF
	Images     []engineImage
	ImagesErr  error
}

func readEngineSnapshot(ctx context.Context, api EngineAPI) (engineSnapshot, error) {
	var snap engineSnapshot
	var ver engineVersion
	if err := api.Get(ctx, "/version", &ver); err != nil {
		return snap, err
	}
	snap.APIVersion = ver.APIVersion
	if err := api.Get(ctx, "/info", &snap.Info); err != nil {
		return snap, err
	}
	if err := api.Get(ctx, "/system/df", &snap.DF); err != nil {
		return snap, err
	}
	// Manifests are optional enrichment (foreign-platform residue). A failure
	// here marks that bucket unavailable; it does not fail the section.
	snap.ImagesErr = api.Get(ctx, "/images/json?manifests=1", &snap.Images)
	return snap, nil
}

// buildRuntimeBuckets turns one snapshot into closed-vocabulary buckets plus
// local-only image refs. engineArch is the normalized engine architecture.
// Any absent field or negative figure makes the affected bucket unavailable
// (parse_failed); a bucket is never measured on fabricated zeros.
func buildRuntimeBuckets(snap engineSnapshot, engineArch string) ([]RuntimeBucket, []RuntimeImageRef) {
	var refs []RuntimeImageRef
	df := snap.DF
	images, imagesOK := validDFImages(df.Images)

	buckets := []RuntimeBucket{imagesBucket(df.LayersSize, images, imagesOK)}
	buckets = append(buckets, buildCacheBucket(df.BuildCache))

	if imagesOK {
		var dCount, dBytes int64
		for _, im := range images {
			if isDangling(im.RepoTags) && im.Containers == 0 {
				dCount++
				dBytes += imageUnique(im)
				refs = append(refs, RuntimeImageRef{Ref: shortImageID(im.ID), Bucket: BucketDanglingImages, UniqueBytes: imageUnique(im)})
			}
		}
		buckets = append(buckets, RuntimeBucket{Kind: BucketDanglingImages, Status: ProbeMeasured,
			Count: i64p(dCount), Bytes: i64p(dBytes), UniqueBytes: i64p(dBytes)})
	} else {
		buckets = append(buckets, unavailableBucket(BucketDanglingImages, ReasonParseFailed))
	}

	buckets = append(buckets, foreignPlatformBucket(snap, engineArch, &refs))

	if imagesOK {
		sCount, sBytes, sRefs := supersededTags(images)
		refs = append(refs, sRefs...)
		buckets = append(buckets, RuntimeBucket{Kind: BucketSupersededTags, Status: ProbeMeasured,
			Count: i64p(sCount), Bytes: i64p(sBytes), UniqueBytes: i64p(sBytes)})
	} else {
		buckets = append(buckets, unavailableBucket(BucketSupersededTags, ReasonParseFailed))
	}

	buckets = append(buckets, volumesBucket(df.Volumes))

	sort.SliceStable(refs, func(i, j int) bool { return refs[i].UniqueBytes > refs[j].UniqueBytes })
	if len(refs) > maxLocalImageRefs {
		refs = refs[:maxLocalImageRefs]
	}
	return buckets, refs
}

func unavailableBucket(kind, reason string) RuntimeBucket {
	return RuntimeBucket{Kind: kind, Status: ProbeUnavailable, Reason: reason}
}

// validDFImages rejects an absent list or any negative size. SharedSize -1
// is the engine's "not computed" marker and is allowed (unique = whole size).
func validDFImages(p *[]engineDFImage) ([]engineDFImage, bool) {
	if p == nil {
		return nil, false
	}
	for _, im := range *p {
		if im.Size < 0 || im.SharedSize < -1 || im.Containers < 0 {
			return nil, false
		}
	}
	return *p, true
}

func imagesBucket(layers *int64, images []engineDFImage, ok bool) RuntimeBucket {
	if !ok || layers == nil || *layers < 0 {
		return unavailableBucket(BucketImages, ReasonParseFailed)
	}
	var unique, inUse int64
	for _, im := range images {
		unique += imageUnique(im)
		if im.Containers > 0 {
			inUse++
		}
	}
	shared := *layers - unique
	if shared < 0 {
		shared = 0
	}
	return RuntimeBucket{Kind: BucketImages, Status: ProbeMeasured,
		Count: i64p(int64(len(images))), Bytes: i64p(*layers),
		UniqueBytes: i64p(unique), SharedBytes: i64p(shared), InUse: i64p(inUse)}
}

func buildCacheBucket(p *[]engineDFCache) RuntimeBucket {
	if p == nil {
		return unavailableBucket(BucketBuildCache, ReasonParseFailed)
	}
	var total, reclaim int64
	for _, c := range *p {
		if c.Size < 0 {
			return unavailableBucket(BucketBuildCache, ReasonParseFailed)
		}
		total += c.Size
		if !c.InUse && !c.Shared {
			reclaim += c.Size
		}
	}
	return RuntimeBucket{Kind: BucketBuildCache, Status: ProbeMeasured,
		Count: i64p(int64(len(*p))), Bytes: i64p(total), ReclaimableBytes: i64p(reclaim)}
}

func volumesBucket(p *[]engineDFVolume) RuntimeBucket {
	if p == nil {
		return unavailableBucket(BucketVolumesUnreferenced, ReasonParseFailed)
	}
	var count, total int64
	for _, v := range *p {
		if v.UsageData == nil || v.UsageData.Size < 0 || v.UsageData.RefCount < 0 {
			return unavailableBucket(BucketVolumesUnreferenced, ReasonParseFailed)
		}
		if v.UsageData.RefCount == 0 {
			count++
			total += v.UsageData.Size
		}
	}
	return RuntimeBucket{Kind: BucketVolumesUnreferenced, Status: ProbeMeasured, Count: i64p(count), Bytes: i64p(total)}
}

func foreignPlatformBucket(snap engineSnapshot, engineArch string, refs *[]RuntimeImageRef) RuntimeBucket {
	if snap.ImagesErr != nil {
		reason := ReasonProbeFailed
		if errors.Is(snap.ImagesErr, errEngineParse) {
			reason = ReasonParseFailed
		}
		return RuntimeBucket{Kind: BucketForeignPlatform, Status: ProbeUnavailable, Reason: reason}
	}
	if engineArch == "" || engineArch == "other" {
		return RuntimeBucket{Kind: BucketForeignPlatform, Status: ProbeUnavailable, Reason: ReasonUnsupportedEngine}
	}
	withManifests := 0
	for _, im := range snap.Images {
		if im.Manifests != nil {
			withManifests++
		}
	}
	if len(snap.Images) > 0 && withManifests == 0 {
		// Engine too old for manifests (API < 1.47): residue is unknown.
		return RuntimeBucket{Kind: BucketForeignPlatform, Status: ProbeUnavailable, Reason: ReasonUnsupportedEngine}
	}
	var count, bytes int64
	for _, im := range snap.Images {
		ref := firstTag(im.RepoTags)
		for _, m := range im.Manifests {
			if m.Kind != "image" || !m.Available || m.ImageData == nil {
				continue
			}
			arch := normalizeArch(m.ImageData.Platform.Architecture)
			if arch == "" || arch == engineArch {
				continue
			}
			if m.Size.Total < 0 {
				return unavailableBucket(BucketForeignPlatform, ReasonParseFailed)
			}
			count++
			bytes += m.Size.Total
			if ref != "" {
				*refs = append(*refs, RuntimeImageRef{Ref: ref, Bucket: BucketForeignPlatform,
					Platform: m.ImageData.Platform.OS + "/" + m.ImageData.Platform.Architecture, UniqueBytes: m.Size.Total})
			}
		}
	}
	return RuntimeBucket{Kind: BucketForeignPlatform, Status: ProbeMeasured, Count: i64p(count), Bytes: i64p(bytes)}
}

// supersededTags finds, per repository, semver-tagged images older than the
// newest semver tag and not used by a container. Non-semver tags (latest,
// local, validate) are never judged superseded.
func supersededTags(images []engineDFImage) (int64, int64, []RuntimeImageRef) {
	type tagged struct {
		ver [3]int
		ref string
		im  engineDFImage
	}
	byRepo := map[string][]tagged{}
	for _, im := range images {
		for _, rt := range namedTags(im.RepoTags) {
			repo, tag, ok := splitRepoTag(rt)
			if !ok {
				continue
			}
			if v, ok := parseSemverTag(tag); ok {
				byRepo[repo] = append(byRepo[repo], tagged{v, rt, im})
			}
		}
	}
	// An image ID that is the newest semver in any repo, or carries any
	// non-semver tag, is still wanted: removing an older tag only untags it.
	keep := map[string]bool{}
	for _, im := range images {
		for _, rt := range namedTags(im.RepoTags) {
			_, tag, ok := splitRepoTag(rt)
			if !ok {
				continue
			}
			if _, semver := parseSemverTag(tag); !semver {
				keep[im.ID] = true
			}
		}
	}
	for _, ts := range byRepo {
		newest := ts[0]
		for _, t := range ts[1:] {
			if semverLess(newest.ver, t.ver) {
				newest = t
			}
		}
		keep[newest.im.ID] = true
	}
	var count, bytes int64
	var refs []RuntimeImageRef
	seen := map[string]bool{}
	for _, ts := range byRepo {
		if len(ts) < 2 {
			continue
		}
		sort.Slice(ts, func(i, j int) bool { return semverLess(ts[j].ver, ts[i].ver) })
		newestID := ts[0].im.ID
		for _, t := range ts[1:] {
			if t.im.Containers > 0 || t.im.ID == newestID || keep[t.im.ID] || seen[t.im.ID] {
				continue
			}
			seen[t.im.ID] = true
			count++
			u := imageUnique(t.im)
			bytes += u
			refs = append(refs, RuntimeImageRef{Ref: t.ref, Bucket: BucketSupersededTags, UniqueBytes: u})
		}
	}
	return count, bytes, refs
}

func imageUnique(im engineDFImage) int64 {
	if im.SharedSize < 0 || im.SharedSize > im.Size {
		return im.Size
	}
	return im.Size - im.SharedSize
}

// namedTags drops digest references (repo@sha256:…) and the legacy
// <none>:<none> placeholder; engines list digests in RepoTags too.
func namedTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t == "<none>:<none>" || strings.Contains(t, "@") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// isDangling is true when an image has no name:tag reference, including an
// image kept only by digest after its tag moved to a newer image.
func isDangling(tags []string) bool {
	return len(namedTags(tags)) == 0
}

func firstTag(tags []string) string {
	named := namedTags(tags)
	if len(named) == 0 {
		return ""
	}
	return named[0]
}

func shortImageID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return "sha256:" + id
}

// splitRepoTag splits at the last colon after the last slash so registry
// ports (host:5000/repo:tag) stay in the repository.
func splitRepoTag(rt string) (string, string, bool) {
	if strings.Contains(rt, "@") {
		return "", "", false
	}
	slash := strings.LastIndex(rt, "/")
	colon := strings.LastIndex(rt, ":")
	if colon <= slash || colon == len(rt)-1 {
		return "", "", false
	}
	return rt[:colon], rt[colon+1:], true
}

// parseSemverTag accepts vMAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH with no
// pre-release or build suffix; anything else is not judged.
func parseSemverTag(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func semverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func i64p(n int64) *int64 { return &n }
