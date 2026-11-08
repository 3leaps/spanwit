package space

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Image families group tagged images by repository so the report can say
// which images are older builds of something newer. It answers the question
// an operator otherwise needs local knowledge for ("three versions of the
// same tool; the old two can go"). It is informational: containers are the
// only use it can see, so compose files and Dockerfile FROM pins are not
// checked, and nothing here is called safe to remove.
//
// Families come from /images/json, not /system/df, so they survive a broken
// disk-usage call. Names never reach the aggregate; they appear only in
// local-disclosure image refs.

// engineContainer is the subset of /containers/json?all=1 used for in-use.
type engineContainer struct {
	ImageID string `json:"ImageID"`
	State   string `json:"State"`
}

// runningStates hold their image in use; any other state (exited, created,
// dead) frees it once the container is removed.
var runningStates = map[string]bool{"running": true, "paused": true, "restarting": true, "removing": true}

var shortHexID = regexp.MustCompile(`^sha256:[0-9a-f]{12}$`)

// versionTag accepts a full three-part version with an optional variant
// suffix: v1.2.3, 1.94.1, 1.94.1-bookworm, 1.94.1-slim-bookworm. Partial
// versions (3, 22-bookworm, 1.94) float to the newest release of their line,
// so they cannot be ordered against a full version; like local, diagnostic,
// latest and feature names they are never ranked, and an image carrying one
// is kept. Build time alone cannot tell an older build from a different image
// that happens to share a repository.
var versionTag = regexp.MustCompile(`^v?(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-([a-z][a-z0-9.]*(?:-[a-z0-9.]+)*))?$`)

type parsedVersion struct {
	v       [3]int
	variant string
}

func parseVersionTag(tag string) (parsedVersion, bool) {
	m := versionTag.FindStringSubmatch(tag)
	if m == nil {
		return parsedVersion{}, false
	}
	var p parsedVersion
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return parsedVersion{}, false
		}
		p.v[i] = n
	}
	p.variant = m[4]
	return p, true
}

type familyMember struct {
	img engineImage
	ver [3]int
	tag string
}

type familyVerdict struct {
	older     bool
	ambiguous bool
}

// buildImageFamilies ranks tagged native-platform images within families of
// one repository, one variant and one major version line (node:22-bookworm
// and node:24-bookworm are parallel lines, not versions of one another). An
// image is an older sibling only when it is older in every family it is in
// and carries no tag that is not a version (latest, local, ...). unique maps
// image ID to engine-reported unique bytes when /system/df answered.
func buildImageFamilies(snap engineSnapshot, engineArch string, df dfFacts) (ImageFamilies, []RuntimeImageRef) {
	unique := df.unique
	if snap.ImagesErr != nil {
		reason, status := engineCallReason(snap.ImagesErr)
		return ImageFamilies{Status: ProbeUnavailable, Reason: reason, HTTPStatus: status}, nil
	}
	for _, im := range snap.Images {
		if im.Size < 0 || im.Created < 0 {
			return ImageFamilies{Status: ProbeUnavailable, Reason: ReasonParseFailed}, nil
		}
	}

	byKey := map[string]map[string]*familyMember{}
	keyRepo := map[string]string{}
	nonVersion := map[string]bool{}
	images := map[string]engineImage{}
	for _, im := range snap.Images {
		if foreignOnly(im, engineArch) {
			continue
		}
		for _, rt := range namedTags(im.RepoTags) {
			repo, tag, ok := splitRepoTag(rt)
			if !ok {
				nonVersion[im.ID] = true
				continue
			}
			images[im.ID] = im
			pv, ok := parseVersionTag(tag)
			if !ok {
				nonVersion[im.ID] = true
				continue
			}
			key := repo + "\x00" + pv.variant + "\x00" + strconv.Itoa(pv.v[0])
			keyRepo[key] = repo
			fam := byKey[key]
			if fam == nil {
				fam = map[string]*familyMember{}
				byKey[key] = fam
			}
			m := fam[im.ID]
			if m == nil {
				m = &familyMember{img: im, ver: pv.v, tag: tag}
				fam[im.ID] = m
			} else if semverLess(m.ver, pv.v) {
				m.ver, m.tag = pv.v, tag
			}
		}
	}

	out := ImageFamilies{Status: ProbeMeasured}
	verdicts := map[string][]familyVerdict{}
	firstRef := map[string]string{}
	var families, ambiguous int64
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fam := byKey[key]
		if len(fam) < 2 {
			for id := range fam {
				verdicts[id] = append(verdicts[id], familyVerdict{})
			}
			continue
		}
		families++
		members := make([]*familyMember, 0, len(fam))
		for _, m := range fam {
			members = append(members, m)
		}
		byVersion := orderMembers(members, func(a, b *familyMember) bool {
			if a.ver != b.ver {
				return semverLess(b.ver, a.ver)
			}
			return a.img.ID < b.img.ID
		})
		byCreated := orderMembers(members, func(a, b *familyMember) bool {
			if a.img.Created != b.img.Created {
				return a.img.Created > b.img.Created
			}
			return a.img.ID < b.img.ID
		})
		// A rebuilt old tag, one version on two images, or a build-time tie
		// leaves the order unknown: nothing is ranked.
		amb := !sameOrder(byVersion, byCreated) || versionTie(byVersion) || createdTie(byCreated)
		if amb {
			ambiguous++
		}
		for i, m := range byVersion {
			verdicts[m.img.ID] = append(verdicts[m.img.ID], familyVerdict{older: i > 0 && !amb, ambiguous: amb})
			if i > 0 && firstRef[m.img.ID] == "" {
				firstRef[m.img.ID] = keyRepo[key] + ":" + m.tag
			}
		}
	}
	out.Families = i64p(families)
	out.OrderAmbiguous = i64p(ambiguous)

	// Older in every family it belongs to, and no tag outside a version line.
	var candidates []string
	for id, vs := range verdicts {
		older := !nonVersion[id]
		for _, v := range vs {
			older = older && v.older && !v.ambiguous
		}
		if older {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)

	if snap.ContainersErr != nil {
		// Use is unknown: no image is ranked as an older sibling.
		reason, _ := engineCallReason(snap.ContainersErr)
		out.InUseStatus, out.InUseReason = ProbeUnavailable, reason
		return out, nil
	}
	// Every container must resolve to a listed image. One that does not (an
	// image ID in another form, or an image missing from the list) could be
	// using any candidate, so nothing is ranked.
	listed := map[string]bool{}
	for _, im := range snap.Images {
		listed[normalImageID(im.ID)] = true
	}
	for _, c := range snap.Containers {
		if c.ImageID == "" || !listed[normalImageID(c.ImageID)] {
			out.InUseStatus, out.InUseReason = ProbeUnavailable, ReasonEngineDisagreement
			return out, nil
		}
	}
	running, exitedOnly := imageUse(snap.Containers)
	out.InUseStatus = ProbeMeasured

	var count, exitedCount, localCount int64
	var uniqueSum, sizeSum int64
	allUnique := true
	var refs []RuntimeImageRef
	for _, id := range candidates {
		im := images[id]
		switch {
		case running[normalImageID(id)]:
			continue
		case exitedOnly[normalImageID(id)]:
			exitedCount++
			continue
		case df.containers[normalImageID(id)] > 0:
			// The engine's own count says something uses it, even though the
			// container list did not show it: held.
			continue
		}
		count++
		if locallyBuilt(im) {
			localCount++
		}
		sizeSum += im.Size
		u, ok := unique[normalImageID(id)]
		if !ok {
			allUnique = false
		}
		uniqueSum += u
		ref := RuntimeImageRef{Ref: firstRef[id], Bucket: BucketOlderSibling, UniqueBytes: im.Size, Bound: BoundUpper}
		if ok {
			ref.UniqueBytes, ref.Bound = u, BoundExact
		}
		if sid := shortImageID(id); shortHexID.MatchString(sid) {
			ref.ID = sid
		}
		refs = append(refs, ref)
	}
	out.OlderSiblings = i64p(count)
	out.LocallyBuilt = i64p(localCount)
	out.InUseByExitedOnly = i64p(exitedCount)
	// Per-image unique bytes miss layers shared only among the candidates, so
	// their sum is a lower bound; per-image sizes double-count shared layers,
	// so their sum is an upper bound.
	if allUnique {
		out.OlderSiblingBytes, out.BytesBound = i64p(uniqueSum), BoundLower
	} else {
		out.OlderSiblingBytes, out.BytesBound = i64p(sizeSum), BoundUpper
	}
	return out, refs
}

func orderMembers(ms []*familyMember, less func(a, b *familyMember) bool) []*familyMember {
	out := append([]*familyMember(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func sameOrder(a, b []*familyMember) bool {
	for i := range a {
		if a[i].img.ID != b[i].img.ID {
			return false
		}
	}
	return true
}

// createdTie is true when the two newest members share a build time: the
// current member is then unknown.
func createdTie(order []*familyMember) bool {
	return len(order) > 1 && order[0].img.Created == order[1].img.Created
}

// foreignOnly is true when manifests are known and no available image
// manifest matches the engine: that image is foreign-platform residue, not a
// sibling of the native build.
func foreignOnly(im engineImage, engineArch string) bool {
	if engineArch == "" || engineArch == ArchOther {
		return false
	}
	seen := false
	for _, m := range im.Manifests {
		if m.Kind != "image" || !m.Available || m.ImageData == nil {
			continue
		}
		seen = true
		if normalizeArch(m.ImageData.Platform.Architecture) == engineArch {
			return false
		}
	}
	return seen
}

func versionTie(order []*familyMember) bool {
	for i := 1; i < len(order); i++ {
		if order[i].ver == order[i-1].ver {
			return true
		}
	}
	return false
}

// locallyBuilt marks an image whose loss means a rebuild, not a re-pull: a
// :local tag, or no registry digest at all.
func locallyBuilt(im engineImage) bool {
	if len(im.RepoDigests) == 0 {
		return true
	}
	for _, rt := range namedTags(im.RepoTags) {
		if strings.HasSuffix(rt, ":local") {
			return true
		}
	}
	return false
}

// imageUse splits images used by containers into running (held) and used
// only by stopped containers (freed once those are removed).
func imageUse(cs []engineContainer) (running, exitedOnly map[string]bool) {
	running, exitedOnly = map[string]bool{}, map[string]bool{}
	for _, c := range cs {
		if runningStates[c.State] {
			running[normalImageID(c.ImageID)] = true
		}
	}
	for _, c := range cs {
		if id := normalImageID(c.ImageID); !running[id] {
			exitedOnly[id] = true
		}
	}
	return running, exitedOnly
}

// normalImageID compares image IDs with or without the sha256: prefix.
func normalImageID(id string) string {
	return strings.ToLower(strings.TrimPrefix(id, "sha256:"))
}

func countExited(cs []engineContainer) int64 {
	var n int64
	for _, c := range cs {
		if !runningStates[c.State] {
			n++
		}
	}
	return n
}
