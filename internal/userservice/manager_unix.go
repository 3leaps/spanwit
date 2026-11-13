//go:build darwin || linux

package userservice

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3leaps/spanwit/internal/serviceexec"
)

const (
	launchctlProgram = "/bin/launchctl"
	systemctlProgram = "/usr/bin/systemctl"
	managerTimeout   = 10 * time.Second
	managerMaxOutput = 64 << 10
)

func managerNameForPlatform(platform string) (string, error) {
	switch platform {
	case PlatformDarwin:
		return "launchd", nil
	case PlatformLinux:
		return "systemd-user", nil
	default:
		return "", fmt.Errorf("unsupported user-service platform %q", platform)
	}
}

func buildManagerEnv(platform, home, configHome string, uid int) []string {
	env := []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	}
	for _, key := range []string{"USER", "LOGNAME"} {
		if val := os.Getenv(key); validEnvValue(val, 256) {
			env = append(env, key+"="+val)
		}
	}
	if platform == PlatformLinux {
		if configHome != "" && validatePath("config home", configHome, maxPathBytes) == nil {
			env = append(env, "XDG_CONFIG_HOME="+configHome)
		}
		rtDir := os.Getenv("XDG_RUNTIME_DIR")
		if validatePath("runtime dir", rtDir, maxPathBytes) != nil {
			candidate := fmt.Sprintf("/run/user/%d", uid)
			if st, err := os.Lstat(candidate); err == nil && st.IsDir() {
				rtDir = candidate
			} else {
				rtDir = ""
			}
		}
		if rtDir != "" {
			env = append(env, "XDG_RUNTIME_DIR="+rtDir)
		}
		if bus := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); validDBusAddress(bus) {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS="+bus)
		}
	}
	return env
}

func validEnvValue(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, invalidPathRune) < 0
}

func validDBusAddress(value string) bool {
	if !strings.HasPrefix(value, "unix:path=/") || strings.ContainsRune(value, ';') {
		return false
	}
	pathPart := strings.TrimPrefix(value, "unix:path=")
	return validatePath("dbus socket", pathPart, maxPathBytes) == nil
}

func inspectManager(ctx context.Context, runner serviceexec.Runner, platform, label, home, configHome string, uid int) ManagerSummary {
	mgrName, err := managerNameForPlatform(platform)
	if err != nil {
		return ManagerSummary{State: "unknown", Detail: clipDetail(err.Error())}
	}
	if label == "" || home == "" || uid < 0 {
		return ManagerSummary{Manager: mgrName, Label: label, State: "not_queried"}
	}
	if runner == nil {
		runner = serviceexec.ExecRunner{}
	}
	env := buildManagerEnv(platform, home, configHome, uid)
	switch platform {
	case PlatformDarwin:
		target := fmt.Sprintf("gui/%d/%s", uid, label)
		res := runner.Run(ctx, serviceexec.Request{
			Program:   launchctlProgram,
			Args:      []string{"print", target},
			Dir:       home,
			Env:       env,
			Timeout:   managerTimeout,
			MaxOutput: managerMaxOutput,
		})
		return parseLaunchdPrint(label, res)
	case PlatformLinux:
		unit := label + ".service"
		res := runner.Run(ctx, serviceexec.Request{
			Program:   systemctlProgram,
			Args:      []string{"--user", "show", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,MainPID,ExecMainPID,ExecMainStatus,UnitFileState", "--", unit},
			Dir:       home,
			Env:       env,
			Timeout:   managerTimeout,
			MaxOutput: managerMaxOutput,
		})
		return parseSystemdShow(label, res)
	default:
		return ManagerSummary{Manager: mgrName, Label: label, State: "unknown", Detail: "unsupported platform"}
	}
}

func parseLaunchdPrint(label string, res serviceexec.Result) ManagerSummary {
	summary := ManagerSummary{Manager: "launchd", Label: label, State: "unknown"}
	switch res.Outcome {
	case serviceexec.NotStarted, serviceexec.TimedOut, serviceexec.Canceled, serviceexec.OutputLimit, serviceexec.Uncertain:
		summary.Detail = clipResultDetail("launchctl print did not complete cleanly", res)
		return summary
	case serviceexec.Failed:
		combined := string(res.Stdout) + "\n" + string(res.Stderr)
		if res.ExitCode == 113 || strings.Contains(combined, "Could not find service") {
			summary.State = "not_loaded"
			return summary
		}
		summary.Detail = clipResultDetail(fmt.Sprintf("launchctl print exited %d without not-found confirmation", res.ExitCode), res)
		return summary
	case serviceexec.Success:
		if res.Uncertain {
			summary.Detail = clipResultDetail("launchctl print returned uncertain cleanup", res)
			return summary
		}
	}
	var stateVal string
	var pidVal int
	var hasPID bool
	var lastExit *int
	for _, rawLine := range strings.Split(string(res.Stdout), "\n") {
		if !strings.HasPrefix(rawLine, "\t") || strings.HasPrefix(rawLine, "\t\t") {
			continue
		}
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "state = "):
			stateVal = strings.TrimSpace(strings.TrimPrefix(line, "state = "))
		case strings.HasPrefix(line, "pid = "):
			rawPID := strings.TrimSpace(strings.TrimPrefix(line, "pid = "))
			parsed, err := strconv.Atoi(rawPID)
			if err != nil || parsed <= 0 {
				summary.Detail = fmt.Sprintf("invalid launchd pid %q", rawPID)
				return summary
			}
			pidVal = parsed
			hasPID = true
		case strings.HasPrefix(line, "last exit code = "):
			rawExit := strings.TrimSpace(strings.TrimPrefix(line, "last exit code = "))
			if parsed, err := strconv.Atoi(rawExit); err == nil {
				code := parsed
				lastExit = &code
			}
		}
	}
	if stateVal == "" {
		summary.Detail = "launchctl print output missing state field"
		return summary
	}
	summary.SubState = clipToken(stateVal, 128)
	summary.ExitCode = lastExit
	switch stateVal {
	case "running":
		if !hasPID || pidVal <= 0 {
			summary.Detail = "launchd reported running state without a valid PID"
			return summary
		}
		summary.State = "running"
		summary.PID = pidVal
		return summary
	case "waiting", "not running":
		if hasPID {
			summary.Detail = "launchd reported inactive state with an active PID"
			return summary
		}
		if lastExit != nil && *lastExit != 0 {
			summary.State = "failed"
			return summary
		}
		summary.State = "stopped"
		return summary
	default:
		summary.Detail = fmt.Sprintf("unclassified launchd state %q", stateVal)
		return summary
	}
}

func parseSystemdShow(label string, res serviceexec.Result) ManagerSummary {
	summary := ManagerSummary{Manager: "systemd-user", Label: label, State: "unknown"}
	if res.Outcome != serviceexec.Success || res.Uncertain {
		summary.Detail = clipResultDetail("systemctl --user show did not complete cleanly", res)
		return summary
	}
	props := make(map[string]string, 8)
	for _, rawLine := range strings.Split(string(res.Stdout), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[k] = v
	}
	loadState, okLoad := props["LoadState"]
	activeState, okActive := props["ActiveState"]
	subState, okSub := props["SubState"]
	rawPID, okPID := props["MainPID"]
	if !okLoad || !okActive || !okSub || !okPID {
		summary.Detail = "systemctl show output missing required state properties"
		return summary
	}
	mainPID, err := strconv.Atoi(rawPID)
	if err != nil || mainPID < 0 {
		summary.Detail = fmt.Sprintf("invalid systemd MainPID %q", rawPID)
		return summary
	}
	if rawExit, ok := props["ExecMainStatus"]; ok && activeState != "active" {
		if parsed, err := strconv.Atoi(rawExit); err == nil {
			code := parsed
			summary.ExitCode = &code
		}
	}
	if subState != "" {
		summary.SubState = clipToken(subState, 128)
	}
	switch {
	case loadState == "not-found" && activeState == "inactive" && mainPID == 0:
		summary.State = "not_loaded"
		return summary
	case activeState == "active" && subState == "running":
		if mainPID <= 0 {
			summary.Detail = "systemd reported active/running without a positive MainPID"
			return summary
		}
		summary.State = "running"
		summary.PID = mainPID
		return summary
	case activeState == "inactive" && subState == "dead" && mainPID == 0:
		summary.State = "stopped"
		return summary
	case activeState == "failed" && mainPID == 0:
		summary.State = "failed"
		return summary
	default:
		summary.Detail = clipDetail(fmt.Sprintf("systemd state LoadState=%s ActiveState=%s SubState=%s MainPID=%d", loadState, activeState, subState, mainPID))
		return summary
	}
}

func startStepRequests(r serviceReceipt, currentManagerState string) ([]serviceexec.Request, error) {
	env := buildManagerEnv(r.Platform, r.Home, r.ConfigHome, r.UID)
	switch r.Platform {
	case PlatformDarwin:
		if currentManagerState != "not_loaded" {
			return nil, fmt.Errorf("launchd service is already loaded in state %q; run stop first so launchd unloads any cached definition before starting", currentManagerState)
		}
		domain := fmt.Sprintf("gui/%d", r.UID)
		target := domain + "/" + r.Label
		return []serviceexec.Request{
			{
				Program:   launchctlProgram,
				Args:      []string{"bootstrap", domain, r.DefinitionPath},
				Dir:       r.Home,
				Env:       env,
				Timeout:   managerTimeout,
				MaxOutput: managerMaxOutput,
			},
			{
				Program:   launchctlProgram,
				Args:      []string{"kickstart", "-p", target},
				Dir:       r.Home,
				Env:       env,
				Timeout:   managerTimeout,
				MaxOutput: managerMaxOutput,
			},
		}, nil
	case PlatformLinux:
		unit := r.Label + ".service"
		return []serviceexec.Request{
			{
				Program:   systemctlProgram,
				Args:      []string{"--user", "daemon-reload"},
				Dir:       r.Home,
				Env:       env,
				Timeout:   managerTimeout,
				MaxOutput: managerMaxOutput,
			},
			{
				Program:   systemctlProgram,
				Args:      []string{"--user", "start", "--", unit},
				Dir:       r.Home,
				Env:       env,
				Timeout:   managerTimeout,
				MaxOutput: managerMaxOutput,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported user-service platform %q", r.Platform)
	}
}

func stopStepRequest(r serviceReceipt) (serviceexec.Request, error) {
	env := buildManagerEnv(r.Platform, r.Home, r.ConfigHome, r.UID)
	switch r.Platform {
	case PlatformDarwin:
		target := fmt.Sprintf("gui/%d/%s", r.UID, r.Label)
		return serviceexec.Request{
			Program:   launchctlProgram,
			Args:      []string{"bootout", target},
			Dir:       r.Home,
			Env:       env,
			Timeout:   managerTimeout,
			MaxOutput: managerMaxOutput,
		}, nil
	case PlatformLinux:
		return serviceexec.Request{
			Program:   systemctlProgram,
			Args:      []string{"--user", "stop", "--", r.Label + ".service"},
			Dir:       r.Home,
			Env:       env,
			Timeout:   managerTimeout,
			MaxOutput: managerMaxOutput,
		}, nil
	default:
		return serviceexec.Request{}, fmt.Errorf("unsupported user-service platform %q", r.Platform)
	}
}

func reloadStepRequest(r serviceReceipt) (serviceexec.Request, bool) {
	if r.Platform != PlatformLinux {
		return serviceexec.Request{}, false
	}
	env := buildManagerEnv(r.Platform, r.Home, r.ConfigHome, r.UID)
	return serviceexec.Request{
		Program:   systemctlProgram,
		Args:      []string{"--user", "daemon-reload"},
		Dir:       r.Home,
		Env:       env,
		Timeout:   managerTimeout,
		MaxOutput: managerMaxOutput,
	}, true
}

func summarizeCommand(req serviceexec.Request, res serviceexec.Result) CommandSummary {
	argv := append([]string{req.Program}, req.Args...)
	summary := CommandSummary{
		Argv:      argv,
		Outcome:   res.Outcome,
		Started:   res.Started,
		Uncertain: res.Uncertain,
		ExitCode:  res.ExitCode,
	}
	if res.Outcome != serviceexec.Success || res.Uncertain {
		summary.Detail = clipResultDetail("command execution did not complete cleanly", res)
	}
	return summary
}

func clipResultDetail(prefix string, res serviceexec.Result) string {
	var parts []string
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if res.Err != nil {
		parts = append(parts, res.Err.Error())
	}
	if stderr := strings.TrimSpace(string(res.Stderr)); stderr != "" {
		parts = append(parts, stderr)
	} else if stdout := strings.TrimSpace(string(res.Stdout)); stdout != "" {
		parts = append(parts, stdout)
	}
	return clipDetail(strings.Join(parts, ": "))
}

func clipDetail(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if invalidPathRune(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	return clipToken(cleaned, 1024)
}

func clipToken(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	trimmed := value[:maxBytes]
	for !utf8.ValidString(trimmed) && len(trimmed) > 0 {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}
