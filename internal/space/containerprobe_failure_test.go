//go:build !windows

package space

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/3leaps/spanwit/internal/capacity"
)

// A df body as a damaged engine returns it: it names guest paths, which must
// never reach the report.
const damagedDFBody = `{"message":"failed to calculate image disk usage: lstat /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/26695/fs: no such file or directory"}`

func dfBucketKinds() []string {
	return []string{BucketImages, BucketBuildCache, BucketDanglingImages, BucketSupersededTags, BucketVolumesUnreferenced}
}

func reportJSON(t *testing.T, rt *ContainerRuntime) string {
	t.Helper()
	raw, err := json.Marshal(rt)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCollectContainerRuntime_DFFailureDegradesOnlyDFBuckets(t *testing.T) {
	eng := fakeColimaEngine()
	eng.fail["/system/df"] = &engineStatusError{Path: "/system/df", Status: 500}
	probed := false
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		ProbeVM: true, Env: colimaEnv(t), API: func(string) EngineAPI { return eng },
		Runner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			probed = true
			return []byte(fakeDFOutput), nil
		}),
	})
	if rt.Engine != EngineColima || rt.Detection.Status != DetectionIdentified || rt.Detection.Reason != "" || rt.Detection.HTTPStatus != nil {
		t.Fatalf("df failure must not touch detection: %+v", rt.Detection)
	}
	if !probed || rt.GuestFilesystem.Status != ProbeMeasured {
		t.Fatalf("guest probe must still run: probed=%v %+v", probed, rt.GuestFilesystem)
	}
	for _, kind := range dfBucketKinds() {
		b := bucketByKind(t, rt, kind)
		if b.Status != ProbeUnavailable || b.Reason != ReasonEngineError || b.HTTPStatus == nil || *b.HTTPStatus != 500 || b.Bytes != nil || b.Count != nil {
			t.Fatalf("%s: want unavailable/engine_error/500 with no figures, got %+v", kind, b)
		}
	}
	if f := bucketByKind(t, rt, BucketForeignPlatform); f.Status != ProbeMeasured {
		t.Fatalf("foreign platform comes from /images/json and must stay measured: %+v", f)
	}
	if len(rt.Buckets) != 6 || rt.Buckets[0].Kind != BucketImages || rt.Buckets[5].Kind != BucketVolumesUnreferenced {
		t.Fatalf("bucket set and order must match a measured run: %+v", rt.Buckets)
	}
	if strings.Contains(reportJSON(t, rt), ReasonEngineUnreachable) {
		t.Fatal("engine_unreachable must not appear when the engine answered")
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestCollectContainerRuntime_VersionOrInfoErrorIsUnidentified(t *testing.T) {
	for _, path := range []string{"/version", "/info"} {
		t.Run(path, func(t *testing.T) {
			eng := fakeColimaEngine()
			eng.fail[path] = &engineStatusError{Path: path, Status: 500}
			rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
				Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
			d := rt.Detection
			if d.Status != DetectionUnidentified || d.Reason != ReasonEngineError || d.HTTPStatus == nil || *d.HTTPStatus != 500 {
				t.Fatalf("want unidentified/engine_error/500, got %+v", d)
			}
			if len(rt.Buckets) != 0 || rt.GuestFilesystem.Reason != ReasonEngineError {
				t.Fatalf("nothing to corroborate: %+v", rt)
			}
			requireRuntimeSchemaValid(t, rt)
		})
	}
}

func TestCollectContainerRuntime_ImplausibleStatusIsOmitted(t *testing.T) {
	for _, code := range []int{0, 42, 99, 600, 999, -1} {
		eng := fakeColimaEngine()
		eng.fail["/system/df"] = &engineStatusError{Path: "/system/df", Status: code}
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		b := bucketByKind(t, rt, BucketImages)
		if b.Reason != ReasonEngineError || b.HTTPStatus != nil {
			t.Fatalf("status %d: want engine_error without http_status, got %+v", code, b)
		}
		requireRuntimeSchemaValid(t, rt)
	}
	for _, code := range []int{100, 599} {
		if s := httpStatusOf(&engineStatusError{Status: code}); s == nil || *s != code {
			t.Fatalf("status %d must be kept", code)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestCollectContainerRuntime_ClientTimeoutIsTimeout(t *testing.T) {
	eng := fakeColimaEngine()
	eng.fail["/version"] = &net.OpError{Op: "dial", Net: "unix", Err: timeoutErr{}}
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
	if rt.Detection.Reason != ReasonTimeout {
		t.Fatalf("want timeout, got %+v", rt.Detection)
	}
}

// shortTempDir keeps unix socket paths under the 104-byte sun_path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sw029-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func colimaEnvAt(home string) EngineEnv {
	return EngineEnv{Home: home, GOOS: "darwin", EvalSymlinks: noSymlinks,
		DockerHost: "unix://" + home + "/.colima/default/docker.sock"}
}

// serveEngine answers the real socket client: 200 for version and info, a
// damaged df, and an empty image list.
func serveEngine(t *testing.T, sock string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(fakeVersion))
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(fakeInfo))
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(damagedDFBody))
		case strings.HasSuffix(r.URL.Path, "/images/json"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func TestCollectContainerRuntime_RealSocketDamagedDF(t *testing.T) {
	home := shortTempDir(t)
	serveEngine(t, filepath.Join(home, ".colima", "default", "docker.sock"))
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{Env: colimaEnvAt(home)})
	if rt.Detection.Status != DetectionIdentified {
		t.Fatalf("detection: %+v", rt.Detection)
	}
	b := bucketByKind(t, rt, BucketImages)
	if b.Reason != ReasonEngineError || b.HTTPStatus == nil || *b.HTTPStatus != 500 {
		t.Fatalf("images bucket: %+v", b)
	}
	out := reportJSON(t, rt)
	for _, leak := range []string{"snapshots", "containerd", "26695", "lstat", "failed to calculate"} {
		if strings.Contains(out, leak) {
			t.Fatalf("engine error body leaked (%q) into the report: %s", leak, out)
		}
	}
	requireRuntimeSchemaValid(t, rt)
}

func TestCollectContainerRuntime_DialFailures(t *testing.T) {
	t.Run("missing socket on a colima endpoint is stopped", func(t *testing.T) {
		home := shortTempDir(t)
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{Env: colimaEnvAt(home)})
		if rt.Engine != EngineColima || rt.Detection.Reason != ReasonEngineStopped || rt.GuestFilesystem.Reason != ReasonEngineStopped || len(rt.Buckets) != 0 {
			t.Fatalf("want colima engine_stopped, got %+v", rt)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("refused connection on a colima endpoint is stopped", func(t *testing.T) {
		home := shortTempDir(t)
		sock := filepath.Join(home, ".colima", "default", "docker.sock")
		if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = ln.Close() // stale socket file: connect is refused
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{Env: colimaEnvAt(home)})
		if rt.Detection.Reason != ReasonEngineStopped {
			t.Fatalf("want engine_stopped, got %+v", rt.Detection)
		}
	})
	t.Run("permission denied is never stopped", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses socket permissions")
		}
		home := shortTempDir(t)
		sock := filepath.Join(home, ".colima", "default", "docker.sock")
		serveEngine(t, sock)
		if err := os.Chmod(sock, 0); err != nil {
			t.Fatal(err)
		}
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{Env: colimaEnvAt(home)})
		if rt.Detection.Reason != ReasonEngineUnreachable {
			t.Fatalf("EACCES must stay engine_unreachable, got %+v", rt.Detection)
		}
	})
	t.Run("missing socket on an unknown endpoint is unreachable", func(t *testing.T) {
		dir := shortTempDir(t)
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: EngineEnv{Home: t.TempDir(), GOOS: "darwin", EvalSymlinks: noSymlinks, DockerHost: "unix://" + dir + "/engine.sock"}})
		if rt.Engine != EngineUnknown || rt.Detection.Reason != ReasonEngineUnreachable {
			t.Fatalf("unknown engine must not be called stopped: %+v", rt.Detection)
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("plain connect error stays unreachable", func(t *testing.T) {
		eng := fakeColimaEngine()
		eng.fail["/version"] = errors.New("connection reset by peer")
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		if rt.Detection.Reason != ReasonEngineUnreachable {
			t.Fatalf("%+v", rt.Detection)
		}
	})
}

func writeSparseDisk(t *testing.T, home, instance string, size int64) {
	t.Helper()
	dir := filepath.Join(home, ".colima", "_lima", "_disks", instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "datadisk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Symlink(home, filepath.Join(dir, "in_use_by")); err != nil {
		t.Fatal(err)
	}
}

func TestCollectContainerRuntime_HostDisk(t *testing.T) {
	t.Run("stopped engine still reports the host disk", func(t *testing.T) {
		home := shortTempDir(t)
		writeSparseDisk(t, home, "colima", 1<<30)
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{Env: colimaEnvAt(home)})
		h := rt.HostDisk
		if rt.Detection.Reason != ReasonEngineStopped || h == nil || h.Status != ProbeMeasured ||
			*h.Files != 1 || *h.LogicalBytes != 1<<30 || *h.AllocatedBytes >= 1<<30 {
			t.Fatalf("want stopped with a sparse 1 GiB host disk, got %+v / %+v", rt.Detection, h)
		}
		if len(rt.Buckets) != 0 {
			t.Fatalf("no guest buckets for a stopped engine: %+v", rt.Buckets)
		}
		if strings.Contains(reportJSON(t, rt), home) {
			t.Fatal("host path leaked into the report")
		}
		requireRuntimeSchemaValid(t, rt)
	})
	t.Run("named profile uses its own instance", func(t *testing.T) {
		home := shortTempDir(t)
		writeSparseDisk(t, home, "colima", 1<<20)
		writeSparseDisk(t, home, "colima-work", 2<<20)
		h := colimaHostDisk(home, "work")
		if h == nil || *h.LogicalBytes != 2<<20 {
			t.Fatalf("profile work must read colima-work only: %+v", h)
		}
	})
	t.Run("absent disk directory omits the field", func(t *testing.T) {
		eng := fakeColimaEngine()
		rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
			Env: colimaEnv(t), API: func(string) EngineAPI { return eng }})
		if rt.HostDisk != nil {
			t.Fatalf("no disk on this host: %+v", rt.HostDisk)
		}
	})
	t.Run("empty disk directory is unavailable, not zero", func(t *testing.T) {
		home := shortTempDir(t)
		if err := os.MkdirAll(filepath.Join(home, ".colima", "_lima", "_disks", "colima"), 0o700); err != nil {
			t.Fatal(err)
		}
		h := colimaHostDisk(home, "default")
		if h == nil || h.Status != ProbeUnavailable || h.LogicalBytes != nil {
			t.Fatalf("%+v", h)
		}
	})
	t.Run("dot profiles are refused", func(t *testing.T) {
		for _, p := range []string{"", ".", ".."} {
			if colimaHostDisk(shortTempDir(t), p) != nil {
				t.Fatalf("profile %q", p)
			}
		}
	})
}

func TestEngineConnectReason_PerEngineKind(t *testing.T) {
	dialErr := func(errno syscall.Errno) error {
		return &url.Error{Op: "Get", URL: "http://engine/v1.44/version",
			Err: &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}}
	}
	ctx := context.Background()
	cases := []struct {
		engine string
		errno  syscall.Errno
		want   string
	}{
		{EngineColima, syscall.ENOENT, ReasonEngineStopped},
		{EngineOrbStack, syscall.ENOENT, ReasonEngineStopped},
		{EngineDockerDesktop, syscall.ENOENT, ReasonEngineStopped},
		{EnginePodman, syscall.ENOENT, ReasonEngineStopped},
		// A missing native socket may mean Docker is not installed.
		{EngineNative, syscall.ENOENT, ReasonEngineUnreachable},
		{EngineNative, syscall.ECONNREFUSED, ReasonEngineStopped},
		{EngineColima, syscall.ECONNREFUSED, ReasonEngineStopped},
		{EngineColima, syscall.EACCES, ReasonEngineUnreachable},
		{EngineNative, syscall.EACCES, ReasonEngineUnreachable},
		{EngineColima, syscall.ECONNRESET, ReasonEngineUnreachable},
		{EngineUnknown, syscall.ENOENT, ReasonEngineUnreachable},
		{EngineUnknown, syscall.ECONNREFUSED, ReasonEngineUnreachable},
	}
	for _, c := range cases {
		got, status := engineConnectReason(dialErr(c.errno), ctx, EngineEndpoint{Engine: c.engine})
		if got != c.want || status != nil {
			t.Errorf("%s/%v: got %s (status %v), want %s", c.engine, c.errno, got, status, c.want)
		}
	}
}
