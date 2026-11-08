package space

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func img(id string, created, size int64, tags ...string) engineImage {
	return engineImage{ID: id, RepoTags: tags, RepoDigests: []string{"example.test/r@sha256:00"}, Created: created, Size: size}
}

func families(t *testing.T, images []engineImage, containers []engineContainer, unique map[string]int64) (ImageFamilies, []RuntimeImageRef) {
	t.Helper()
	return buildImageFamilies(engineSnapshot{Images: images, Containers: containers}, ArchARM64, dfFacts{unique: unique})
}

func refIDs(refs []RuntimeImageRef) string {
	var ids []string
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return strings.Join(ids, " ")
}

const (
	idA = "sha256:aaaaaaaaaaaa0000"
	idB = "sha256:bbbbbbbbbbbb0000"
	idC = "sha256:cccccccccccc0000"
)

func TestImageFamilies_SemverOlderSibling(t *testing.T) {
	f, refs := families(t, []engineImage{
		img(idA, 300, 1000, "example.test/team/tool:v1.2.0"),
		img(idB, 200, 900, "example.test/team/tool:v1.1.0"),
		img(idC, 100, 800, "example.test/team/tool:v1.0.0"),
	}, []engineContainer{}, nil)
	if *f.Families != 1 || *f.OrderAmbiguous != 0 || *f.OlderSiblings != 2 || *f.OlderSiblingBytes != 1700 || f.BytesBound != BoundUpper {
		t.Fatalf("%+v", f)
	}
	if refIDs(refs) != "sha256:bbbbbbbbbbbb sha256:cccccccccccc" || refs[0].Bound != BoundUpper || refs[0].Bucket != BucketOlderSibling {
		t.Fatalf("refs %+v", refs)
	}
}

func TestImageFamilies_EngineUniqueBytesGiveLowerBound(t *testing.T) {
	f, refs := families(t, []engineImage{
		img(idA, 300, 1000, "r/x:v1.1.0"), img(idB, 200, 900, "r/x:v1.0.0"),
	}, []engineContainer{}, map[string]int64{idA: 100, idB: 40})
	if *f.OlderSiblingBytes != 40 || f.BytesBound != BoundLower || refs[0].UniqueBytes != 40 || refs[0].Bound != BoundExact {
		t.Fatalf("%+v %+v", f, refs)
	}
}

func TestImageFamilies_RebuildResetsCreatedIsAmbiguous(t *testing.T) {
	// v1.0.0 was rebuilt after v1.1.0: version and build-time orders disagree.
	f, refs := families(t, []engineImage{
		img(idA, 100, 1000, "r/x:v1.1.0"), img(idB, 500, 900, "r/x:v1.0.0"),
	}, []engineContainer{}, nil)
	if *f.OrderAmbiguous != 1 || *f.OlderSiblings != 0 || len(refs) != 0 {
		t.Fatalf("an ambiguous family ranks nothing: %+v %+v", f, refs)
	}
}

func TestImageFamilies_TiesAreAmbiguous(t *testing.T) {
	f, _ := families(t, []engineImage{img(idA, 100, 1, "r/x:v1.1.0"), img(idB, 100, 1, "r/x:v1.0.0")}, []engineContainer{}, nil)
	if *f.OrderAmbiguous != 1 || *f.OlderSiblings != 0 {
		t.Fatalf("build-time tie: %+v", f)
	}
	// v1.0.0 and 1.0.0 on two images compare equal: which is newer is unknown.
	f, _ = families(t, []engineImage{img(idA, 200, 1, "r/x:v1.0.0"), img(idB, 100, 1, "r/x:1.0.0")}, []engineContainer{}, nil)
	if *f.OrderAmbiguous != 1 || *f.OlderSiblings != 0 {
		t.Fatalf("version tie: %+v", f)
	}
	// A partial version floats: alpine:3 is not older than alpine:3.21.0.
	f, _ = families(t, []engineImage{img(idA, 200, 1, "alpine:3"), img(idB, 100, 1, "alpine:3.21.0")}, []engineContainer{}, nil)
	if *f.Families != 0 || *f.OlderSiblings != 0 {
		t.Fatalf("partial version: %+v", f)
	}
}

func TestImageFamilies_NonVersionTagsAreNeverRanked(t *testing.T) {
	// Feature-named builds of one repository are different images, not
	// versions: build time alone must not rank them.
	f, _ := families(t, []engineImage{
		img(idA, 300, 1, "r/app:scaffold"), img(idB, 200, 1, "r/app:claims-review"), img(idC, 100, 1, "r/app:local"),
	}, []engineContainer{}, nil)
	if *f.Families != 0 || *f.OlderSiblings != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestImageFamilies_MajorLinesAndVariantsAreSeparate(t *testing.T) {
	f, _ := families(t, []engineImage{
		img(idA, 100, 1, "node:24-bookworm"), img(idB, 300, 1, "node:22-alpine"), img(idC, 200, 1, "node:22-bookworm"),
	}, []engineContainer{}, nil)
	if *f.Families != 0 || *f.OlderSiblings != 0 {
		t.Fatalf("22 and 24, alpine and bookworm are parallel lines: %+v", f)
	}
	f, refs := families(t, []engineImage{
		img(idA, 300, 5, "rust:1.94.1-bookworm"), img(idB, 100, 7, "rust:1.88.0-bookworm"), img(idC, 200, 9, "rust:1.94.1"),
	}, []engineContainer{}, nil)
	if *f.Families != 1 || *f.OlderSiblings != 1 || refs[0].Ref != "rust:1.88.0-bookworm" {
		t.Fatalf("same line and variant ranks: %+v %+v", f, refs)
	}
}

func TestImageFamilies_LocallyBuiltOlderVersion(t *testing.T) {
	old := img(idB, 100, 700, "r/runner:v0.5.9")
	old.RepoDigests = nil
	f, _ := families(t, []engineImage{img(idA, 200, 900, "r/runner:v0.5.10"), old}, []engineContainer{}, nil)
	if *f.OlderSiblings != 1 || *f.LocallyBuilt != 1 {
		t.Fatalf("%+v", f)
	}
}

func TestImageFamilies_ExtraNonVersionTagKeepsImage(t *testing.T) {
	f, _ := families(t, []engineImage{img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0", "r/a:local")}, []engineContainer{}, nil)
	if *f.OlderSiblings != 0 {
		t.Fatalf("an image with a :local tag is kept: %+v", f)
	}
}

func TestImageFamilies_OneImageInTwoFamiliesIsNotOlder(t *testing.T) {
	// idB is older in r/a but the only build in r/b.
	f, _ := families(t, []engineImage{
		img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0", "r/b:v1.0.0"),
	}, []engineContainer{}, nil)
	if *f.Families != 1 || *f.OlderSiblings != 0 {
		t.Fatalf("older in one family but current in another is not a candidate: %+v", f)
	}
}

func TestImageFamilies_LatestIsNeverOlder(t *testing.T) {
	f, _ := families(t, []engineImage{
		img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0", "r/a:latest"),
	}, []engineContainer{}, nil)
	if *f.OlderSiblings != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestImageFamilies_ForeignPlatformIsNotASibling(t *testing.T) {
	foreign := img(idB, 100, 1, "r/a:v1.0.0")
	foreign.Manifests = []engineManifest{manifest("amd64")}
	native := img(idA, 200, 1, "r/a:v2.0.0")
	native.Manifests = []engineManifest{manifest("arm64")}
	f, _ := families(t, []engineImage{native, foreign}, []engineContainer{}, nil)
	if *f.Families != 0 || *f.OlderSiblings != 0 {
		t.Fatalf("foreign residue belongs to foreign_platform, not a family: %+v", f)
	}
}

func manifest(arch string) engineManifest {
	m := engineManifest{Kind: "image", Available: true}
	m.ImageData = &struct {
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
		} `json:"Platform"`
	}{}
	m.ImageData.Platform.Architecture = arch
	m.ImageData.Platform.OS = "linux"
	return m
}

func TestImageFamilies_InUseByImageID(t *testing.T) {
	images := []engineImage{img(idA, 300, 1, "r/a:v1.3.0"), img(idB, 200, 1, "r/a:v1.2.0"), img(idC, 100, 1, "r/a:v1.1.0")}
	// idB runs; idC is used only by a stopped container. The container's
	// image name is irrelevant: ImageID decides.
	f, refs := families(t, images, []engineContainer{
		{ImageID: idB, State: "running"}, {ImageID: idC, State: "exited"},
	}, nil)
	if *f.OlderSiblings != 0 || *f.InUseByExitedOnly != 1 || len(refs) != 0 {
		t.Fatalf("%+v %+v", f, refs)
	}
	// a stopped and a running container on the same image: held.
	f, _ = families(t, images, []engineContainer{{ImageID: idC, State: "exited"}, {ImageID: idC, State: "running"}}, nil)
	if *f.InUseByExitedOnly != 0 || *f.OlderSiblings != 1 {
		t.Fatalf("%+v", f)
	}
}

func TestImageFamilies_ContainersUnknownRanksNothing(t *testing.T) {
	f, refs := buildImageFamilies(engineSnapshot{
		Images:        []engineImage{img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0")},
		ContainersErr: &engineStatusError{Path: "/containers/json", Status: 500},
	}, ArchARM64, dfFacts{})
	if f.InUseStatus != ProbeUnavailable || f.InUseReason != ReasonEngineError || f.OlderSiblings != nil || len(refs) != 0 {
		t.Fatalf("%+v %+v", f, refs)
	}
	if m := olderSiblingEstimate(f); m.Status != ProbeUnavailable || m.Reason != ReasonEngineError {
		t.Fatalf("%+v", m)
	}
}

func TestImageFamilies_ImagesFailureIsUnavailable(t *testing.T) {
	f, _ := buildImageFamilies(engineSnapshot{ImagesErr: &engineStatusError{Status: 700}}, ArchARM64, dfFacts{})
	if f.Status != ProbeUnavailable || f.Reason != ReasonEngineError || f.HTTPStatus != nil {
		t.Fatalf("%+v", f)
	}
	f, _ = buildImageFamilies(engineSnapshot{Images: []engineImage{img(idA, -1, 1, "r/a:v1.0.0")}}, ArchARM64, dfFacts{})
	if f.Status != ProbeUnavailable || f.Reason != ReasonParseFailed {
		t.Fatalf("negative build time must not be ranked: %+v", f)
	}
}

func TestImageFamilies_HostileNamesStayOutOfAggregateAndCommands(t *testing.T) {
	hostile := "r/a:\x1b[31mv1;$(touch pwned)"
	images := []engineImage{img(idA, 200, 1, "r/a:v1.2.0"), img(idB, 100, 1, hostile), img(idC, 75, 1, "r/a:v1.1.0"), img("sha256:zz;rm -rf", 50, 1, "r/a:v1.0.0")}
	f, refs := families(t, images, []engineContainer{}, nil)
	raw, _ := json.Marshal(f)
	if strings.Contains(string(raw), "pwned") || strings.Contains(string(raw), "r/a") {
		t.Fatalf("aggregate must not carry names: %s", raw)
	}
	if *f.OlderSiblings != 2 || len(refs) != 2 {
		t.Fatalf("v1 and v0 are older; the hostile tag is not a version: %+v %+v", f, refs)
	}
	for _, r := range refs {
		if strings.Contains(r.Ref, "pwned") {
			t.Fatalf("a non-version tag must never be ranked: %+v", r)
		}
		if r.Ref == "r/a:v1.0.0" && r.ID != "" {
			t.Fatalf("an invalid engine ID must not become an id: %+v", r)
		}
		if r.ID != "" && !shortHexID.MatchString(r.ID) {
			t.Fatalf("unvalidated id %q", r.ID)
		}
	}
	rt := &ContainerRuntime{Engine: EngineColima, ImageFamilies: &f}
	for _, s := range buildRuntimeRecipe(rt, nil, stoppedSet{}, false) {
		if strings.Contains(strings.Join(s.Command, " "), "pwned") || strings.Contains(strings.Join(s.Command, " "), "sha256") {
			t.Fatalf("recipe commands never interpolate names or ids: %+v", s)
		}
	}
}

func TestCapRefs_TruncatesLongNames(t *testing.T) {
	long := strings.Repeat("é", 300)
	refs := capRefs([]RuntimeImageRef{{Ref: long}})
	if len(refs[0].Ref) > maxImageRefLen || !strings.HasPrefix(long, refs[0].Ref) {
		t.Fatalf("len %d", len(refs[0].Ref))
	}
}

func TestTrimEstimate(t *testing.T) {
	gb := int64(1 << 30)
	guest := GuestFilesystem{Status: ProbeMeasured, UsedBytes: i64p(73 * gb)}
	host := func(alloc int64) *RuntimeHostDisk {
		return &RuntimeHostDisk{Status: ProbeMeasured, AllocatedBytes: i64p(alloc), LogicalBytes: i64p(125 * gb), Files: i64p(1)}
	}
	m, w := trimEstimate(&ContainerRuntime{GuestFilesystem: guest, HostDisk: host(80 * gb)}, ReasonUnsupportedEngine)
	if m.Status != ProbeMeasured || *m.Bytes != 7*gb || m.Bound != BoundUpper || !strings.Contains(w, "Estimate") {
		t.Fatalf("%+v %q", m, w)
	}
	m, w = trimEstimate(&ContainerRuntime{GuestFilesystem: guest, HostDisk: host(70 * gb)}, ReasonUnsupportedEngine)
	if *m.Bytes != 0 || !strings.Contains(w, "Free space inside the VM first") {
		t.Fatalf("negative gap must clamp to zero with the low warning: %+v %q", m, w)
	}
	m, _ = trimEstimate(&ContainerRuntime{GuestFilesystem: guest}, ReasonUnsupportedEngine)
	if m.Status != ProbeUnavailable || m.Reason != ReasonUnsupportedEngine {
		t.Fatalf("missing host side: %+v", m)
	}
	m, _ = trimEstimate(&ContainerRuntime{GuestFilesystem: GuestFilesystem{Status: ProbeNotProbed, Reason: ReasonProbeNotRequested}, HostDisk: host(gb)}, ReasonProbeNotRequested)
	if m.Status != ProbeNotProbed || m.Reason != ReasonProbeNotRequested {
		t.Fatalf("missing guest side: %+v", m)
	}
}

func TestStoppedContainers(t *testing.T) {
	rw := func(n int64) *int64 { return &n }
	snap := engineSnapshot{
		Containers: []engineContainer{{State: "exited"}, {State: "running"}, {State: "created"}},
		DF:         engineDF{Containers: &[]engineDFContainer{{State: "exited", SizeRw: rw(30)}, {State: "running", SizeRw: rw(1000)}, {State: "created", SizeRw: rw(5)}}},
	}
	s := stoppedContainers(snap)
	if *s.count != 2 || s.bytes.Status != ProbeMeasured || *s.bytes.Bytes != 35 || !s.present() {
		t.Fatalf("%+v %+v", s, s.bytes)
	}
	snap.DFErr = errors.New("boom")
	if s := stoppedContainers(snap); s.bytes.Status != ProbeUnavailable || !s.present() {
		t.Fatalf("df failure keeps the count: %+v", s)
	}
	if s := stoppedContainers(engineSnapshot{Containers: []engineContainer{{State: "running"}}, DF: engineDF{Containers: &[]engineDFContainer{}}}); s.present() {
		t.Fatal("no stopped containers: no step")
	}
}

func TestCollectContainerRuntime_FamiliesEndToEnd(t *testing.T) {
	eng := fakeColimaEngine()
	eng.fail["/system/df"] = &engineStatusError{Path: "/system/df", Status: 500}
	eng.responses["/images/json?manifests=1"] = `[
 {"Id":"sha256:aaaaaaaaaaaa1111","RepoTags":["example.test/team/runner:v0.5.14"],"RepoDigests":["example.test/team/runner@sha256:01"],"Created":300,"Size":7000,"Manifests":[]},
 {"Id":"sha256:bbbbbbbbbbbb2222","RepoTags":["example.test/team/runner:v0.5.13"],"RepoDigests":["example.test/team/runner@sha256:02"],"Created":200,"Size":6900,"Manifests":[]},
 {"Id":"sha256:cccccccccccc3333","RepoTags":["example.test/team/runner:v0.5.12"],"RepoDigests":["example.test/team/runner@sha256:03"],"Created":100,"Size":6800,"Manifests":[]}
]`
	eng.responses["/containers/json?all=1"] = `[{"Image":"example.test/team/runner:v0.5.14","ImageID":"sha256:cccccccccccc3333","State":"exited"}]`
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		Env: colimaEnv(t), RefDisclosure: RefDisclosureLocal, API: func(string) EngineAPI { return eng }})
	f := rt.ImageFamilies
	if f == nil || *f.OlderSiblings != 1 || *f.OlderSiblingBytes != 6900 || f.BytesBound != BoundUpper || *f.InUseByExitedOnly != 1 {
		t.Fatalf("df broken: families still come from the image list: %+v", f)
	}
	var steps []string
	for _, s := range rt.Recipe {
		steps = append(steps, s.Step)
	}
	if got := strings.Join(steps, " "); !strings.Contains(got, "dangling_image_prune container_prune build_cache_prune") || !strings.Contains(got, StepOlderSiblingRemove) {
		t.Fatalf("recipe %s", got)
	}
	found := false
	for _, r := range rt.ImageRefs {
		if r.Bucket == BucketOlderSibling {
			found = true
			if r.ID != "sha256:bbbbbbbbbbbb" || r.Ref != "example.test/team/runner:v0.5.13" || r.Bound != BoundUpper {
				t.Fatalf("%+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("no older-sibling ref: %+v", rt.ImageRefs)
	}
	requireRuntimeSchemaValid(t, rt)

	// Default disclosure: no names anywhere.
	rt = CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
	if raw, _ := json.Marshal(rt); strings.Contains(string(raw), "runner") {
		t.Fatalf("aggregate disclosure leaked a name: %s", raw)
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestImageFamilies_UnmatchedContainerImageFailsClosed(t *testing.T) {
	images := []engineImage{img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0")}
	for _, imageID := range []string{"sha256:bbbbbbbbbbbb0000ffff", "", "sha256:dddddddddddd0000"} {
		f, refs := families(t, images, []engineContainer{{ImageID: imageID, State: "running"}}, nil)
		if f.InUseStatus != ProbeUnavailable || f.InUseReason != ReasonEngineDisagreement || f.OlderSiblings != nil || len(refs) != 0 {
			t.Fatalf("container image %q matches no listed image: must rank nothing, got %+v %+v", imageID, f, refs)
		}
	}
	// The same ID without the sha256: prefix resolves.
	f, _ := families(t, images, []engineContainer{{ImageID: strings.TrimPrefix(idB, "sha256:"), State: "running"}}, nil)
	if f.InUseStatus != ProbeMeasured || *f.OlderSiblings != 0 {
		t.Fatalf("prefix-less ID must match and hold the image: %+v", f)
	}
}

func TestImageFamilies_EngineContainerCountHoldsImage(t *testing.T) {
	images := []engineImage{img(idA, 200, 1, "r/a:v1.1.0"), img(idB, 100, 1, "r/a:v1.0.0")}
	f, refs := buildImageFamilies(engineSnapshot{Images: images, Containers: []engineContainer{}}, ArchARM64,
		dfFacts{containers: map[string]int64{idB: 1}})
	if *f.OlderSiblings != 0 || len(refs) != 0 {
		t.Fatalf("df says a container uses idB: %+v %+v", f, refs)
	}
}

func TestDFImageFacts_SharedSizeNotComputedIsNoUniqueFigure(t *testing.T) {
	df := engineDF{Images: &[]engineDFImage{
		{ID: idA, Size: 1000, SharedSize: -1}, {ID: idB, Size: 900, SharedSize: -1}, {ID: idC, Size: 800, SharedSize: 300, Containers: 2},
	}}
	facts := dfImageFacts(df)
	if _, ok := facts.unique[idB]; ok || facts.unique[idC] != 500 || facts.containers[idC] != 2 {
		t.Fatalf("%+v", facts)
	}
	images := []engineImage{img(idA, 200, 1000, "r/a:v1.1.0"), img(idB, 100, 900, "r/a:v1.0.0")}
	f, refs := buildImageFamilies(engineSnapshot{Images: images, Containers: []engineContainer{}}, ArchARM64, facts)
	if *f.OlderSiblingBytes != 900 || f.BytesBound != BoundUpper || refs[0].Bound != BoundUpper {
		t.Fatalf("an image size is an upper bound, never lower or exact: %+v %+v", f, refs)
	}
}
