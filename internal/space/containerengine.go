package space

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Engine detection resolves which container engine answers `docker` without
// running the docker CLI, starting a VM, or needing root. It mirrors the
// docker CLI endpoint precedence (DOCKER_HOST, DOCKER_CONTEXT, currentContext,
// default socket) by reading the CLI's own config, then classifies the socket
// path. Paths and profile names are inputs only; they never reach the report.

// EngineEnv is the slice of process state detection reads. Tests supply it.
type EngineEnv struct {
	Home          string
	DockerHost    string // DOCKER_HOST
	DockerContext string // DOCKER_CONTEXT
	DockerConfig  string // DOCKER_CONFIG (directory); defaults to ~/.docker
	GOOS          string
	// Hostname corroborates a native engine: a local daemon reports the
	// host's own name. Empty means native can never be confirmed.
	Hostname string
	// EvalSymlinks resolves a socket path; nil means filepath.EvalSymlinks.
	EvalSymlinks func(string) (string, error)
}

// EngineEndpoint is a resolved local engine endpoint. SocketPath and Profile
// are internal handles (for dialing and `colima ssh -p`) and are never
// serialized.
type EngineEndpoint struct {
	Engine     string
	SocketPath string
	Profile    string
	// Remote is true for tcp:// and ssh:// hosts; no local probe applies.
	Remote bool
}

var errNoEndpoint = errors.New("no docker endpoint resolved")

// ResolveEngineEndpoint applies the docker CLI precedence and classifies the
// result. It returns errNoEndpoint when nothing resolves to an existing
// local socket or a remote host.
func ResolveEngineEndpoint(env EngineEnv) (EngineEndpoint, error) {
	host, err := resolveDockerHost(env)
	if err != nil {
		return EngineEndpoint{}, err
	}
	return classifyEndpoint(host, env), nil
}

func resolveDockerHost(env EngineEnv) (string, error) {
	if env.DockerHost != "" {
		return env.DockerHost, nil
	}
	configDir := env.DockerConfig
	if configDir == "" {
		configDir = filepath.Join(env.Home, ".docker")
	}
	name := env.DockerContext
	if name == "" {
		name = currentContext(configDir)
	}
	if name != "" && name != "default" {
		host, err := contextHost(configDir, name)
		if err != nil {
			return "", err
		}
		return host, nil
	}
	if env.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine", nil
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return "unix:///var/run/docker.sock", nil
	}
	return "", errNoEndpoint
}

func currentContext(configDir string) string {
	// #nosec G304 G703 -- the docker CLI config the docker CLI itself reads (DOCKER_CONFIG or ~/.docker); read only for currentContext.
	raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return cfg.CurrentContext
}

// contextHost reads the docker endpoint of a named context. The CLI stores
// context metadata under contexts/meta/<sha256(name)>/meta.json.
func contextHost(configDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	p := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	raw, err := os.ReadFile(p) // #nosec G304 -- path derived from the docker CLI context layout
	if err != nil {
		return "", errNoEndpoint
	}
	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return "", errNoEndpoint
	}
	host := meta.Endpoints["docker"].Host
	if host == "" {
		return "", errNoEndpoint
	}
	return host, nil
}

var colimaSocket = regexp.MustCompile(`^\.colima/([A-Za-z0-9._-]+)/docker\.sock$`)

// classifyEndpoint names the engine from the endpoint alone. Engine info
// corroboration happens later (CorroborateEngine).
func classifyEndpoint(host string, env EngineEnv) EngineEndpoint {
	switch {
	case strings.HasPrefix(host, "tcp://"), strings.HasPrefix(host, "ssh://"):
		return EngineEndpoint{Engine: EngineUnknown, Remote: true}
	case strings.HasPrefix(host, "npipe://"):
		return EngineEndpoint{Engine: EngineUnknown, SocketPath: strings.TrimPrefix(host, "npipe://")}
	case !strings.HasPrefix(host, "unix://"):
		return EngineEndpoint{Engine: EngineUnknown}
	}
	sock := strings.TrimPrefix(host, "unix://")
	resolve := env.EvalSymlinks
	if resolve == nil {
		resolve = filepath.EvalSymlinks
	}
	if real, err := resolve(sock); err == nil {
		sock = real
	}
	ep := EngineEndpoint{Engine: EngineUnknown, SocketPath: sock}
	rel, err := filepath.Rel(env.Home, sock)
	if err == nil && !strings.HasPrefix(rel, "..") {
		rel = filepath.ToSlash(rel)
		switch {
		case colimaSocket.MatchString(rel):
			ep.Engine = EngineColima
			ep.Profile = colimaSocket.FindStringSubmatch(rel)[1]
		case rel == ".orbstack/run/docker.sock":
			ep.Engine = EngineOrbStack
		case rel == ".docker/run/docker.sock", rel == ".docker/desktop/docker.sock":
			ep.Engine = EngineDockerDesktop
		case strings.HasPrefix(rel, ".local/share/containers/podman/machine/"):
			ep.Engine = EnginePodman
		}
		return ep
	}
	if strings.Contains(sock, "/podman/") {
		ep.Engine = EnginePodman
		return ep
	}
	if env.GOOS == "linux" && (sock == "/var/run/docker.sock" || sock == "/run/docker.sock") {
		ep.Engine = EngineNative
	}
	return ep
}

// EngineInfo is the subset of the Engine API /info response used to
// corroborate the endpoint classification.
type EngineInfo struct {
	Name            string `json:"Name"`
	OperatingSystem string `json:"OperatingSystem"`
	Architecture    string `json:"Architecture"`
	DockerRootDir   string `json:"DockerRootDir"`
}

// CorroborateEngine checks endpoint classification against engine info. A
// disagreement downgrades the engine to unknown; it never picks a winner.
// Corroboration is positive: colima must report its own profile's name, and a
// native daemon must report this host's name (a forwarded socket reports the
// remote host's name and is therefore unknown, never measured as local).
func CorroborateEngine(ep EngineEndpoint, info EngineInfo, hostname string) (string, bool) {
	name := strings.ToLower(info.Name)
	osName := strings.ToLower(info.OperatingSystem)
	var agrees bool
	switch ep.Engine {
	case EngineColima:
		want := "colima"
		if ep.Profile != "" && ep.Profile != "default" {
			want = "colima-" + strings.ToLower(ep.Profile)
		}
		agrees = name == want
	case EngineOrbStack:
		agrees = name == "orbstack" || strings.Contains(osName, "orbstack")
	case EngineDockerDesktop:
		agrees = name == "docker-desktop" || strings.Contains(osName, "docker desktop")
	case EnginePodman:
		agrees = strings.Contains(name, "podman") || strings.Contains(osName, "fedora coreos")
	case EngineNative:
		h := strings.ToLower(hostname)
		agrees = h != "" && (name == h || strings.SplitN(name, ".", 2)[0] == strings.SplitN(h, ".", 2)[0])
	default:
		return EngineUnknown, false
	}
	if !agrees {
		return EngineUnknown, false
	}
	return ep.Engine, true
}

// normalizeArch maps engine-reported architecture names to the report enum.
func normalizeArch(a string) string {
	switch strings.ToLower(a) {
	case "aarch64", "arm64":
		return ArchARM64
	case "x86_64", "amd64":
		return ArchAMD64
	case "":
		return ""
	default:
		return ArchOther
	}
}
