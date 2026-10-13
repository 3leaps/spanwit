package space

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/capacity"
)

// fakeEngine serves scrubbed Engine API responses keyed by path.
type fakeEngine struct {
	responses map[string]string
	fail      map[string]error
	calls     []string
}

func (f *fakeEngine) Get(_ context.Context, path string, out any) error {
	f.calls = append(f.calls, path)
	if err := f.fail[path]; err != nil {
		return err
	}
	body, ok := f.responses[path]
	if !ok {
		return fmt.Errorf("unexpected path %s", path)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return fmt.Errorf("%w: %v", errEngineParse, err)
	}
	return nil
}

// Scrubbed shapes from a real colima 0.10 / API 1.53 engine. Refs are
// placeholders; no private registry or org names.
const (
	fakeVersion = `{"ApiVersion":"1.53"}`
	fakeInfo    = `{"Name":"colima","OperatingSystem":"Ubuntu 24.04.4 LTS","Architecture":"aarch64","DockerRootDir":"/var/lib/docker"}`
	fakeDF      = `{
 "LayersSize": 30000,
 "Images": [
  {"Id":"sha256:aaaaaaaaaaaaaaaa","RepoTags":["example.test/team/runner:v0.5.7"],"Size":7000,"SharedSize":300,"Containers":0},
  {"Id":"sha256:bbbbbbbbbbbbbbbb","RepoTags":["example.test/team/runner:v0.5.6"],"Size":6900,"SharedSize":300,"Containers":0},
  {"Id":"sha256:cccccccccccccccc","RepoTags":["example.test/team/runner:v0.5.5"],"Size":6800,"SharedSize":300,"Containers":1},
  {"Id":"sha256:dddddddddddddddd","RepoTags":["example.test/team/runner:local","example.test/team/runner:validate"],"Size":7100,"SharedSize":7100,"Containers":0},
  {"Id":"sha256:eeeeeeeeeeeeeeee","RepoTags":[],"Size":500,"SharedSize":0,"Containers":0},
  {"Id":"sha256:ffffffffffffffff","RepoTags":["registry.test:5000/base:1.2.3"],"Size":100,"SharedSize":-1,"Containers":0}
 ],
 "Volumes": [
  {"UsageData":{"RefCount":0,"Size":4000}},
  {"UsageData":{"RefCount":1,"Size":9000}}
 ],
 "BuildCache": [
  {"Size":1000,"InUse":false,"Shared":false},
  {"Size":2000,"InUse":true,"Shared":false},
  {"Size":3000,"InUse":false,"Shared":true}
 ]
}`
	fakeImages = `[
 {"Id":"sha256:aaaaaaaaaaaaaaaa","RepoTags":["example.test/team/node:24"],"Manifests":[
  {"Kind":"image","Available":true,"Size":{"Total":1600},"ImageData":{"Platform":{"os":"linux","architecture":"amd64"}}},
  {"Kind":"image","Available":true,"Size":{"Total":1500},"ImageData":{"Platform":{"os":"linux","architecture":"arm64"}}},
  {"Kind":"attestation","Available":true,"Size":{"Total":10}}
 ]},
 {"Id":"sha256:bbbbbbbbbbbbbbbb","RepoTags":["example.test/team/tool:1"],"Manifests":[
  {"Kind":"image","Available":false,"Size":{"Total":0},"ImageData":{"Platform":{"os":"linux","architecture":"amd64"}}},
  {"Kind":"image","Available":true,"Size":{"Total":900},"ImageData":{"Platform":{"os":"linux","architecture":"arm64"}}}
 ]}
]`
)

func fakeColimaEngine() *fakeEngine {
	return &fakeEngine{responses: map[string]string{
		"/version": fakeVersion, "/info": fakeInfo, "/system/df": fakeDF, "/images/json?manifests=1": fakeImages,
	}, fail: map[string]error{}}
}

func colimaEnv(t *testing.T) EngineEnv {
	t.Helper()
	home := t.TempDir()
	return EngineEnv{Home: home, GOOS: "darwin", EvalSymlinks: noSymlinks,
		DockerHost: "unix://" + home + "/.colima/default/docker.sock"}
}

const fakeDFOutput = "Filesystem         1-blocks        Used   Available Capacity Mounted on\n" +
	"/dev/vdb1     105089261568 64424509440 40664752128      62% /var/lib/docker\n"

func bucketByKind(t *testing.T, rt *ContainerRuntime, kind string) RuntimeBucket {
	t.Helper()
	for _, b := range rt.Buckets {
		if b.Kind == kind {
			return b
		}
	}
	t.Fatalf("missing bucket %s", kind)
	return RuntimeBucket{}
}

func TestCollectContainerRuntime_ColimaBuckets(t *testing.T) {
	eng := fakeColimaEngine()
	var argv []string
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		ProbeVM: true, Env: colimaEnv(t),
		API: func(string) EngineAPI { return eng },
		Runner: capacity.RunnerFunc(func(_ context.Context, p string, a ...string) ([]byte, error) {
			argv = append([]string{p}, a...)
			return []byte(fakeDFOutput), nil
		}),
	})

	if rt.Engine != EngineColima || rt.Detection.Status != DetectionIdentified || rt.EngineArch != "arm64" || rt.APIVersion != "1.53" {
		t.Fatalf("identity: %+v", rt)
	}
	want := []string{"colima", "ssh", "-p", "default", "--", "df", "-P", "-B1", "/var/lib/docker"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Fatalf("guest probe argv = %v (data root must come from engine info)", argv)
	}
	g := rt.GuestFilesystem
	if g.Status != ProbeMeasured || g.Basis != GuestBasisGuestStatfs || *g.AvailBytes != 40664752128 || g.Level != PressureOK {
		t.Fatalf("guest: %+v", g)
	}

	img := bucketByKind(t, rt, BucketImages)
	// unique: 6700+6600+6500+0+500+100 (SharedSize -1 counts whole) = 20400
	if *img.Count != 6 || *img.Bytes != 30000 || *img.UniqueBytes != 20400 || *img.SharedBytes != 9600 || *img.InUse != 1 {
		t.Fatalf("images: %+v", img)
	}
	bc := bucketByKind(t, rt, BucketBuildCache)
	if *bc.Bytes != 6000 || *bc.ReclaimableBytes != 1000 {
		t.Fatalf("build cache: %+v", bc)
	}
	if d := bucketByKind(t, rt, BucketDanglingImages); *d.Count != 1 || *d.Bytes != 500 {
		t.Fatalf("dangling: %+v", d)
	}
	// only the available amd64 manifest counts; arm64 matches the engine
	if f := bucketByKind(t, rt, BucketForeignPlatform); *f.Count != 1 || *f.Bytes != 1600 {
		t.Fatalf("foreign: %+v", f)
	}
	// v0.5.6 superseded; v0.5.5 in use; local/validate never judged
	if s := bucketByKind(t, rt, BucketSupersededTags); *s.Count != 1 || *s.Bytes != 6600 {
		t.Fatalf("superseded: %+v", s)
	}
	if v := bucketByKind(t, rt, BucketVolumesUnreferenced); *v.Count != 1 || *v.Bytes != 4000 {
		t.Fatalf("volumes: %+v", v)
	}

	steps := []string{}
	for _, s := range rt.Recipe {
		steps = append(steps, s.Step)
	}
	wantSteps := "dangling_image_prune build_cache_prune foreign_platform_remove superseded_tag_remove image_prune_all volume_prune guest_trim"
	if strings.Join(steps, " ") != wantSteps {
		t.Fatalf("recipe order = %v", steps)
	}
	for _, s := range rt.Recipe {
		switch s.Step {
		case StepImagePruneAll:
			// unused only: 6700+6600+0+500+100; the in-use v0.5.5 image is excluded
			if *s.Estimate.Bytes != 13900 || s.Estimate.Bound != BoundLower {
				t.Fatalf("prune --all with an image in use = %d %s, want unused-only 13900 lower", *s.Estimate.Bytes, s.Estimate.Bound)
			}
		case StepGuestTrim:
			if strings.Join(s.Command, " ") != "colima ssh -p <profile> -- sudo fstrim -v <data-root>" {
				t.Fatalf("guest trim must address the measured profile by placeholder: %v", s.Command)
			}
		}
	}

	if rt.RefDisclosure != RefDisclosureAggregate || rt.ImageRefs != nil {
		t.Fatalf("default must be aggregate with no refs")
	}
	raw, _ := json.Marshal(rt)
	for _, leak := range []string{"example.test", "registry.test", "default", "docker.sock", "colima ssh -p"} {
		if leak == "default" {
			if strings.Contains(string(raw), `"default"`) {
				t.Fatalf("profile name leaked into aggregate document")
			}
			continue
		}
		if strings.Contains(string(raw), leak) {
			t.Fatalf("aggregate document leaked %q: %s", leak, raw)
		}
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestCollectContainerRuntime_LocalDisclosureListsRefs(t *testing.T) {
	eng := fakeColimaEngine()
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		Env: colimaEnv(t), RefDisclosure: RefDisclosureLocal, API: func(string) EngineAPI { return eng },
	})
	got := map[string]string{}
	for _, r := range rt.ImageRefs {
		got[r.Ref] = r.Bucket
	}
	if got["example.test/team/runner:v0.5.6"] != BucketSupersededTags ||
		got["example.test/team/node:24"] != BucketForeignPlatform ||
		got["sha256:eeeeeeeeeeee"] != BucketDanglingImages {
		t.Fatalf("local refs = %+v", rt.ImageRefs)
	}
	if rt.GuestFilesystem.Status != ProbeNotProbed || rt.GuestFilesystem.Reason != ReasonProbeNotRequested {
		t.Fatalf("no --probe-vm must not run colima ssh: %+v", rt.GuestFilesystem)
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestCollectContainerRuntime_FailuresAreStatusesNeverZero(t *testing.T) {
	t.Run("no endpoint", func(t *testing.T) {
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: EngineEnv{Home: t.TempDir(), GOOS: "darwin", DockerContext: "missing"},
		})
		if rt.Engine != EngineNone || rt.Detection.Reason != ReasonEndpointUnresolved || len(rt.Buckets) != 0 {
			t.Fatalf("%+v", rt)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("engine unreachable", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.fail["/version"] = errors.New("connect: no such file")
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		if rt.Detection.Reason != ReasonEngineUnreachable || len(rt.Buckets) != 0 || rt.GuestFilesystem.Status != ProbeUnavailable {
			t.Fatalf("%+v", rt)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("manifests fail, rest measured", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.fail["/images/json?manifests=1"] = errors.New("status 400")
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		f := bucketByKind(t, rt, BucketForeignPlatform)
		if f.Status != ProbeUnavailable || f.Bytes != nil || f.Count != nil {
			t.Fatalf("foreign must be unavailable, not zero: %+v", f)
		}
		for _, s := range rt.Recipe {
			if s.Step == StepForeignPlatformRemove && s.Estimate.Status != ProbeUnavailable {
				t.Fatalf("estimate must be unavailable: %+v", s)
			}
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("engine too old for manifests", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.responses["/images/json?manifests=1"] = `[{"Id":"sha256:aa","RepoTags":["example.test/team/node:24"]}]`
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		f := bucketByKind(t, rt, BucketForeignPlatform)
		if f.Status != ProbeUnavailable || f.Reason != ReasonUnsupportedEngine || f.Bytes != nil {
			t.Fatalf("absent manifests must be unavailable, not zero: %+v", f)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("engine disagrees with endpoint", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.responses["/info"] = `{"Name":"orbstack","Architecture":"aarch64","DockerRootDir":"/var/lib/docker"}`
		ran := false
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			ProbeVM: true, Env: colimaEnv(t), API: func(string) EngineAPI { return eng },
			Runner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) { ran = true; return nil, nil }),
		})
		if rt.Engine != EngineUnknown || rt.Detection.Reason != ReasonEngineDisagreement || ran {
			t.Fatalf("disagreement must be unknown and never probe: %+v ran=%v", rt, ran)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("guest probe timeout", func(t *testing.T) {
		eng := fakeColimaEngine()
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			ProbeVM: true, Env: colimaEnv(t), API: func(string) EngineAPI { return eng },
			Runner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
				return nil, &capacity.RunError{Code: "timeout"}
			}),
		})
		if rt.GuestFilesystem.Status != ProbeUnavailable || rt.GuestFilesystem.Reason != ReasonTimeout {
			t.Fatalf("%+v", rt.GuestFilesystem)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("remote host is never dialed", func(t *testing.T) {
		dialed := false
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: EngineEnv{Home: t.TempDir(), DockerHost: "tcp://10.0.0.5:2376"},
			API: func(string) EngineAPI { dialed = true; return fakeColimaEngine() },
		})
		if dialed || rt.Engine != EngineUnknown {
			t.Fatalf("remote: %+v dialed=%v", rt, dialed)
		}
		requireRuntimeSchemaValid(t, rt)
	})
}

func TestCollectContainerRuntime_NativeUsesHostStatfsWithoutProbeFlag(t *testing.T) {
	eng := fakeColimaEngine()
	eng.responses["/info"] = `{"Name":"build-host","OperatingSystem":"Debian GNU/Linux 12","Architecture":"x86_64","DockerRootDir":"/var/lib/docker"}`
	var statted string
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		Env: EngineEnv{Home: "/home/op", GOOS: "linux", DockerHost: "unix:///var/run/docker.sock", EvalSymlinks: noSymlinks, Hostname: "build-host"},
		API: func(string) EngineAPI { return eng },
		Statfs: func(p string) (int64, int64, error) {
			statted = p
			return 100 << 30, 5 << 30, nil
		},
	})
	g := rt.GuestFilesystem
	if rt.Engine != EngineNative || statted != "/var/lib/docker" || g.Basis != GuestBasisHostStatfs || g.Level != PressureCritical {
		t.Fatalf("native: engine=%s statted=%q guest=%+v", rt.Engine, statted, g)
	}
	// amd64 engine: the amd64 manifest is native, the arm64 ones are foreign
	if f := bucketByKind(t, rt, BucketForeignPlatform); *f.Count != 2 || *f.Bytes != 2400 {
		t.Fatalf("foreign on amd64: %+v", f)
	}
	for _, s := range rt.Recipe {
		if s.Step == StepGuestTrim || s.Step == StepDiskGrow {
			t.Fatalf("native engine gets no VM steps: %+v", rt.Recipe)
		}
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestCollectContainerRuntime_HostileEngineShapesNeverFabricate(t *testing.T) {
	cases := map[string]struct {
		df       string
		images   string
		wantBad  []string
		wantGood []string
	}{
		"negative image size": {
			df: `{"LayersSize":100,"Images":[{"Id":"a","RepoTags":["example.test/x:v1.0.1"],"Size":-1,"SharedSize":-1},
				{"Id":"b","RepoTags":["example.test/x:v1.0.0"],"Size":10,"SharedSize":0}],"Volumes":[],"BuildCache":[]}`,
			wantBad:  []string{BucketImages, BucketDanglingImages, BucketSupersededTags},
			wantGood: []string{BucketBuildCache, BucketVolumesUnreferenced},
		},
		"missing LayersSize": {
			df:       `{"Images":[],"Volumes":[],"BuildCache":[]}`,
			wantBad:  []string{BucketImages},
			wantGood: []string{BucketDanglingImages, BucketBuildCache},
		},
		"null lists": {
			df:      `{"LayersSize":0,"Images":null,"Volumes":null,"BuildCache":null}`,
			wantBad: []string{BucketImages, BucketDanglingImages, BucketSupersededTags, BucketVolumesUnreferenced, BucketBuildCache},
		},
		"negative cache and volume": {
			df:       `{"LayersSize":0,"Images":[],"Volumes":[{"UsageData":{"RefCount":0,"Size":-5}}],"BuildCache":[{"Size":-1}]}`,
			wantBad:  []string{BucketBuildCache, BucketVolumesUnreferenced},
			wantGood: []string{BucketImages},
		},
		"volume without usage data": {
			df:      `{"LayersSize":0,"Images":[],"Volumes":[{}],"BuildCache":[]}`,
			wantBad: []string{BucketVolumesUnreferenced},
		},
		"negative manifest size": {
			df:      `{"LayersSize":0,"Images":[],"Volumes":[],"BuildCache":[]}`,
			images:  `[{"Id":"a","RepoTags":["example.test/x:1"],"Manifests":[{"Kind":"image","Available":true,"Size":{"Total":-3},"ImageData":{"Platform":{"os":"linux","architecture":"amd64"}}}]}]`,
			wantBad: []string{BucketForeignPlatform},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eng := fakeColimaEngine()
			eng.responses["/system/df"] = c.df
			if c.images != "" {
				eng.responses["/images/json?manifests=1"] = c.images
			}
			rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
				Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
			for _, k := range c.wantBad {
				if b := bucketByKind(t, rt, k); b.Status != ProbeUnavailable || b.Bytes != nil || b.Count != nil {
					t.Fatalf("%s must be unavailable, got %+v", k, b)
				}
			}
			for _, k := range c.wantGood {
				if b := bucketByKind(t, rt, k); b.Status != ProbeMeasured {
					t.Fatalf("%s should stay measured, got %+v", k, b)
				}
			}
			requireRuntimeSchemaValid(t, rt)
		})
	}
}

func TestSupersededTags_AliasedImageIsNotCounted(t *testing.T) {
	images := []engineDFImage{
		{ID: "new", RepoTags: []string{"example.test/x:v2.0.0"}, Size: 10},
		// v1.0.0 is also tagged :stable, so removing the old tag frees nothing
		{ID: "old", RepoTags: []string{"example.test/x:v1.0.0", "example.test/x:stable"}, Size: 9},
		// same image newest in another repo
		{ID: "shared", RepoTags: []string{"example.test/x:v1.5.0", "example.test/y:v9.0.0"}, Size: 8},
		{ID: "gone", RepoTags: []string{"example.test/x:v0.9.0"}, Size: 7},
	}
	count, bytes, refs := supersededTags(images)
	if count != 1 || bytes != 7 || len(refs) != 1 || refs[0].Ref != "example.test/x:v0.9.0" {
		t.Fatalf("got count=%d bytes=%d refs=%+v", count, bytes, refs)
	}
}

func TestParseDFBytes(t *testing.T) {
	if _, _, _, ok := parseDFBytes([]byte(fakeDFOutput)); !ok {
		t.Fatal("valid df output must parse")
	}
	for _, bad := range []string{"", "Filesystem\n", fakeDFOutput + "/dev/vdc1 1 1 1 1% /x\n",
		"Filesystem 1-blocks Used Available Capacity Mounted on\n/dev/vdb1 10G 5G 5G 50% /var/lib/docker\n",
		"Filesystem 1-blocks Used Available Capacity Mounted on\n/dev/vdb1 -1 0 0 0% /x\n"} {
		if _, _, _, ok := parseDFBytes([]byte(bad)); ok {
			t.Fatalf("must reject %q", bad)
		}
	}
}

func TestSemverAndRepoTagParsing(t *testing.T) {
	if r, tag, ok := splitRepoTag("registry.test:5000/team/base:1.2.3"); !ok || r != "registry.test:5000/team/base" || tag != "1.2.3" {
		t.Fatalf("got %q %q %v", r, tag, ok)
	}
	if _, _, ok := splitRepoTag("registry.test:5000/team/base"); ok {
		t.Fatal("registry port is not a tag")
	}
	for tag, want := range map[string]bool{"v0.5.7": true, "1.2.3": true, "v1.2": false, "1.2.3-rc1": false,
		"latest": false, "01.2.3": false, "local": false} {
		if _, ok := parseSemverTag(tag); ok != want {
			t.Errorf("parseSemverTag(%q) = %v", tag, ok)
		}
	}
}

func requireRuntimeSchemaValid(t *testing.T, rt *ContainerRuntime) {
	t.Helper()
	v1, v2 := validateBoth(t, runtimeDoc(t, rt))
	if v1 != nil || v2 != nil {
		raw, _ := json.MarshalIndent(rt, "", " ")
		t.Fatalf("collected section must validate: v1=%v v2=%v\n%s", v1, v2, raw)
	}
}

// Engines list digest references alongside tags in RepoTags. A digest must
// never be read as a tag, and an image kept only by digest is untagged.
func TestRepoTagsWithDigestReferences(t *testing.T) {
	images := []engineDFImage{
		{ID: "new", RepoTags: []string{"example.test/team/runner:v0.5.9", "example.test/team/runner@sha256:4444"}, Size: 14, SharedSize: 1},
		{ID: "old", RepoTags: []string{"example.test/team/runner:v0.5.7", "example.test/team/runner@sha256:5555"}, Size: 7, SharedSize: 1},
		{ID: "digestonly", RepoTags: []string{"example.test/team/runner@sha256:6666"}, Size: 7, SharedSize: 1},
	}
	count, bytes, refs := supersededTags(images)
	if count != 1 || bytes != 6 || refs[0].Ref != "example.test/team/runner:v0.5.7" {
		t.Fatalf("superseded with digest refs: count=%d bytes=%d refs=%+v", count, bytes, refs)
	}
	if !isDangling(images[2].RepoTags) || isDangling(images[0].RepoTags) {
		t.Fatal("digest-only image is untagged; a tagged image is not")
	}
	if firstTag(images[2].RepoTags) != "" || firstTag(images[0].RepoTags) != "example.test/team/runner:v0.5.9" {
		t.Fatal("firstTag must skip digest references")
	}
}

// colima joins the remote command for the guest shell; an engine-reported
// data root with shell syntax must never reach the probe.
func TestProbeGuest_RejectsUnsafeDataRoot(t *testing.T) {
	for _, root := range []string{
		"/var/lib/docker; id", "/var/lib/$(id)", "/var/lib/`id`", "/var/lib/docker data",
		"/var/lib/docker|cat", "/var/lib/../etc", "/var/-rf", "var/lib/docker", "", "/var/lib/do'cker",
		"/var/lib/docker\nid", "/" + strings.Repeat("a", 300),
	} {
		eng := fakeColimaEngine()
		eng.responses["/info"] = fmt.Sprintf(`{"Name":"colima","Architecture":"aarch64","DockerRootDir":%q}`, root)
		ran := false
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			ProbeVM: true, Env: colimaEnv(t), API: func(string) EngineAPI { return eng },
			Runner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) { ran = true; return nil, nil }),
		})
		if ran || rt.GuestFilesystem.Status != ProbeUnavailable || rt.GuestFilesystem.Reason != ReasonParseFailed {
			t.Fatalf("data root %q: ran=%v guest=%+v", root, ran, rt.GuestFilesystem)
		}
		requireRuntimeSchemaValid(t, rt)
	}
	for _, ok := range []string{"/var/lib/docker", "/var/lib/docker/", "/mnt/lima-colima/docker", "/data/docker_root.v2"} {
		if !safeDataRoot(ok) {
			t.Fatalf("plain path %q must be accepted", ok)
		}
	}
}

func recipeStep(t *testing.T, rt *ContainerRuntime, step string) RuntimeRecipeStep {
	t.Helper()
	for _, s := range rt.Recipe {
		if s.Step == step {
			return s
		}
	}
	t.Fatalf("missing recipe step %s", step)
	return RuntimeRecipeStep{}
}

// Two unused images share a 500-byte layer that neither counts as unique.
// Removing both frees it too, so per-image unique sums are lower bounds.
func TestRecipeEstimateBounds(t *testing.T) {
	sharedOnlyAmongUnused := `{
 "LayersSize": 2500,
 "Images": [
  {"Id":"sha256:1111111111111111","RepoTags":["example.test/team/a:v1.0.1"],"Size":1500,"SharedSize":500,"Containers":0},
  {"Id":"sha256:2222222222222222","RepoTags":["example.test/team/a:v1.0.0"],"Size":1500,"SharedSize":500,"Containers":0}
 ],
 "Volumes": [{"UsageData":{"RefCount":0,"Size":40}}],
 "BuildCache": [{"Size":70,"InUse":false,"Shared":false}]
}`
	t.Run("none in use: prune --all is exact from LayersSize", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.responses["/system/df"] = sharedOnlyAmongUnused
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		all := recipeStep(t, rt, StepImagePruneAll)
		if all.Estimate.Bound != BoundExact || *all.Estimate.Bytes != 2500 {
			t.Fatalf("prune --all: %+v", all.Estimate)
		}
		sup := recipeStep(t, rt, StepSupersededTagRemove)
		if sup.Estimate.Bound != BoundLower || *sup.Estimate.Bytes != 1000 {
			t.Fatalf("superseded estimate must be a lower bound: %+v", sup.Estimate)
		}
		if b := recipeStep(t, rt, StepBuildCachePrune).Estimate; b.Bound != BoundExact {
			t.Fatalf("build cache reclaimable is engine-reported: %+v", b)
		}
		for _, step := range []string{StepVolumePrune, StepForeignPlatformRemove} {
			for _, s := range rt.Recipe {
				if s.Step == step && s.Estimate.Status == ProbeMeasured && s.Estimate.Bound != BoundUpper {
					t.Fatalf("%s must be an upper bound: %+v", step, s.Estimate)
				}
			}
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("one in use: prune --all is a lower bound below the true reclaim", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.responses["/system/df"] = strings.Replace(sharedOnlyAmongUnused,
			`"Size":1500,"SharedSize":500,"Containers":0}
 ],`, `"Size":1500,"SharedSize":500,"Containers":0},
  {"Id":"sha256:3333333333333333","RepoTags":["example.test/team/b:1"],"Size":200,"SharedSize":0,"Containers":1}
 ],`, 1)
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		all := recipeStep(t, rt, StepImagePruneAll)
		// true reclaim of the two unused images is 2500 (incl. their shared layer)
		if all.Estimate.Bound != BoundLower || *all.Estimate.Bytes != 2000 || *all.Estimate.Bytes >= 2500 {
			t.Fatalf("prune --all with an image in use: %+v", all.Estimate)
		}
		requireRuntimeSchemaValid(t, rt)
	})
}
