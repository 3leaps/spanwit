package space

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noSymlinks(p string) (string, error) { return p, nil }

func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveEngineEndpoint_Precedence(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".docker")
	writeContext(t, cfg, "colima", "unix://"+home+"/.colima/default/docker.sock")
	writeContext(t, cfg, "orbstack", "unix://"+home+"/.orbstack/run/docker.sock")
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"currentContext":"colima"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := EngineEnv{Home: home, GOOS: "darwin", EvalSymlinks: noSymlinks}

	t.Run("current context", func(t *testing.T) {
		ep, err := ResolveEngineEndpoint(base)
		if err != nil || ep.Engine != EngineColima || ep.Profile != "default" {
			t.Fatalf("got %+v, %v", ep, err)
		}
	})
	t.Run("DOCKER_CONTEXT beats currentContext", func(t *testing.T) {
		env := base
		env.DockerContext = "orbstack"
		ep, err := ResolveEngineEndpoint(env)
		if err != nil || ep.Engine != EngineOrbStack {
			t.Fatalf("got %+v, %v", ep, err)
		}
	})
	t.Run("DOCKER_HOST beats contexts", func(t *testing.T) {
		env := base
		env.DockerContext = "orbstack"
		env.DockerHost = "unix://" + home + "/.colima/work/docker.sock"
		ep, err := ResolveEngineEndpoint(env)
		if err != nil || ep.Engine != EngineColima || ep.Profile != "work" {
			t.Fatalf("got %+v, %v", ep, err)
		}
	})
	t.Run("missing named context fails closed", func(t *testing.T) {
		env := base
		env.DockerContext = "nope"
		if _, err := ResolveEngineEndpoint(env); !errors.Is(err, errNoEndpoint) {
			t.Fatalf("want errNoEndpoint, got %v", err)
		}
	})
}

func TestClassifyEndpoint_EngineClasses(t *testing.T) {
	// "~/" is expanded against a synthetic home at test time.
	home := filepath.Join(string(filepath.Separator), "home", "op")
	cases := []struct {
		host, goos, want string
		remote           bool
	}{
		{"unix://~/.colima/default/docker.sock", "darwin", EngineColima, false},
		{"unix://~/.orbstack/run/docker.sock", "darwin", EngineOrbStack, false},
		{"unix://~/.docker/run/docker.sock", "darwin", EngineDockerDesktop, false},
		{"unix://~/.local/share/containers/podman/machine/podman.sock", "darwin", EnginePodman, false},
		{"unix:///run/user/1000/podman/podman.sock", "linux", EnginePodman, false},
		{"unix:///var/run/docker.sock", "linux", EngineNative, false},
		{"unix:///var/run/docker.sock", "darwin", EngineUnknown, false},
		{"unix://~/.colima/../../etc/docker.sock", "darwin", EngineUnknown, false},
		{"unix://~/.colima/default/sub/docker.sock", "darwin", EngineUnknown, false},
		{"tcp://10.0.0.5:2376", "darwin", EngineUnknown, true},
		{"ssh://builder", "linux", EngineUnknown, true},
	}
	for _, c := range cases {
		host := strings.Replace(c.host, "~", home, 1)
		ep := classifyEndpoint(host, EngineEnv{Home: home, GOOS: c.goos, EvalSymlinks: func(p string) (string, error) {
			return filepath.Clean(p), nil
		}})
		if ep.Engine != c.want || ep.Remote != c.remote {
			t.Errorf("%s on %s: got %+v, want %s remote=%v", c.host, c.goos, ep, c.want, c.remote)
		}
	}
}

func TestClassifyEndpoint_FollowsSymlinkToRealEngine(t *testing.T) {
	// /var/run/docker.sock on macOS is often a symlink into an engine's home.
	home := filepath.Join(string(filepath.Separator), "home", "op")
	ep := classifyEndpoint("unix:///var/run/docker.sock", EngineEnv{Home: home, GOOS: "darwin",
		EvalSymlinks: func(string) (string, error) { return filepath.Join(home, ".orbstack", "run", "docker.sock"), nil }})
	if ep.Engine != EngineOrbStack {
		t.Fatalf("got %+v", ep)
	}
}

func TestCorroborateEngine_DisagreementIsUnknown(t *testing.T) {
	cases := []struct {
		name     string
		ep       EngineEndpoint
		info     EngineInfo
		hostname string
		want     string
		ok       bool
	}{
		{"colima default", EngineEndpoint{Engine: EngineColima, Profile: "default"}, EngineInfo{Name: "colima"}, "", EngineColima, true},
		{"colima named profile", EngineEndpoint{Engine: EngineColima, Profile: "work"}, EngineInfo{Name: "colima-work"}, "", EngineColima, true},
		{"socket of profile a forwards to b", EngineEndpoint{Engine: EngineColima, Profile: "a"}, EngineInfo{Name: "colima-b"}, "", EngineUnknown, false},
		{"default socket answered by named profile", EngineEndpoint{Engine: EngineColima, Profile: "default"}, EngineInfo{Name: "colima-work"}, "", EngineUnknown, false},
		{"colima path, orbstack info", EngineEndpoint{Engine: EngineColima, Profile: "default"}, EngineInfo{Name: "orbstack"}, "", EngineUnknown, false},
		{"native matches hostname", EngineEndpoint{Engine: EngineNative}, EngineInfo{Name: "build-01"}, "build-01", EngineNative, true},
		{"native short vs fqdn", EngineEndpoint{Engine: EngineNative}, EngineInfo{Name: "build-01.lan"}, "build-01", EngineNative, true},
		{"native socket forwarded from another host", EngineEndpoint{Engine: EngineNative}, EngineInfo{Name: "buildbox"}, "laptop", EngineUnknown, false},
		{"native socket forwarded from a VM", EngineEndpoint{Engine: EngineNative}, EngineInfo{Name: "lima-rancher-desktop"}, "laptop", EngineUnknown, false},
		{"native without hostname never confirms", EngineEndpoint{Engine: EngineNative}, EngineInfo{Name: ""}, "", EngineUnknown, false},
		{"info alone never names an engine", EngineEndpoint{Engine: EngineUnknown}, EngineInfo{Name: "colima"}, "", EngineUnknown, false},
	}
	for _, c := range cases {
		got, ok := CorroborateEngine(c.ep, c.info, c.hostname)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %s %v, want %s %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
