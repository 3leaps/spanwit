//go:build darwin || linux

// Package userservice renders bounded, manual-start service definitions.
//
// It deliberately does not inspect, write, register, start, stop, or enable
// a host service. A later lifecycle adapter owns every host action.
package userservice

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/3leaps/spanwit/internal/daemon"
)

const (
	PlatformDarwin = "darwin"
	PlatformLinux  = "linux"
	maxPathBytes   = 4096
	maxLabelBytes  = 240
)

// Config is host-independent input. All paths are already-resolved absolute
// paths. Executable eligibility is deliberately a caller responsibility.
type Config struct {
	Platform   string
	Executable string
	Home       string
	ConfigHome string // Linux user config root; empty uses Home/.config.
	StateDir   string
	Path       string
	Roots      []string
	Interval   time.Duration
	BinaryName string
	Vendor     string
	UID        int
}

// Plan is a pure rendering result. The false effect flags make explicit that
// rendering neither writes a definition nor changes the service manager.
const PlanSchema = "https://schemas.3leaps.dev/spanwit/user-service-plan/v0.json"

type Plan struct {
	Schema            string     `json:"$schema"`
	Version           int        `json:"version"`
	Manager           string     `json:"manager"`
	Label             string     `json:"label"`
	DefinitionPath    string     `json:"definition_path"`
	Content           string     `json:"content"`
	StartCommands     [][]string `json:"start_commands"`
	StopCommands      [][]string `json:"stop_commands"`
	DefinitionWritten bool       `json:"definition_written"`
	CommandsExecuted  bool       `json:"commands_executed"`
}

// Validate rejects ambiguous definition inputs without touching the host.
func Validate(config Config) error {
	if config.Platform != PlatformDarwin && config.Platform != PlatformLinux {
		return fmt.Errorf("unsupported user-service platform %q", config.Platform)
	}
	if config.UID < 0 {
		return fmt.Errorf("user-service UID must not be negative")
	}
	for _, namedPath := range []struct{ name, value string }{
		{"executable", config.Executable}, {"home", config.Home},
		{"state directory", config.StateDir}, {"sample path", config.Path},
	} {
		if err := validatePath(namedPath.name, namedPath.value, maxPathBytes); err != nil {
			return err
		}
	}
	if config.ConfigHome != "" {
		if err := validatePath("config home", config.ConfigHome, maxPathBytes); err != nil {
			return err
		}
	}
	if config.Interval < time.Minute {
		return fmt.Errorf("daemon interval must be at least %s", time.Minute)
	}
	if !validLabelPart(config.Vendor) || !validLabelPart(config.BinaryName) {
		return fmt.Errorf("vendor and binary name must contain only ASCII letters, digits, dots, underscores, or hyphens")
	}
	if len(config.Vendor)+len(config.BinaryName)+len("..daemon") > maxLabelBytes {
		return fmt.Errorf("user-service label exceeds %d bytes", maxLabelBytes)
	}
	if len(config.Roots) > daemon.MaxAttributionRoots {
		return fmt.Errorf("attribution roots exceed maximum of %d", daemon.MaxAttributionRoots)
	}
	encodedBytes := 0
	seen := make([]string, 0, len(config.Roots))
	for _, root := range config.Roots {
		if err := validatePath("attribution root", root, 1024); err != nil {
			return err
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			return fmt.Errorf("encode attribution root: %w", err)
		}
		encodedBytes += len(encoded)
		if encodedBytes > 16<<10 {
			return fmt.Errorf("encoded attribution roots exceed 16 KiB")
		}
		for _, prior := range seen {
			if containsRoot(prior, root) || containsRoot(root, prior) {
				return fmt.Errorf("overlapping attribution roots")
			}
		}
		seen = append(seen, root)
	}
	return nil
}

// Render validates and renders a platform definition. It omits launchd
// RunAtLoad/KeepAlive and systemd Restart/Install settings: manual lifecycle
// invocation is required for every start.
func Render(config Config) (Plan, error) {
	plan, err := render(config)
	if err != nil {
		return Plan{}, err
	}
	plan.Schema = PlanSchema
	raw, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, err
	}
	// The CLI JSON encoder appends one newline to this document.
	if len(raw)+1 > 128<<10 {
		return Plan{}, fmt.Errorf("service preview exceeds 128 KiB")
	}
	return plan, nil
}

func render(config Config) (Plan, error) {
	if err := Validate(config); err != nil {
		return Plan{}, err
	}
	label := config.Vendor + "." + config.BinaryName + ".daemon"
	args := daemonArgs(config)
	switch config.Platform {
	case PlatformDarwin:
		domain := fmt.Sprintf("gui/%d", config.UID)
		target := domain + "/" + label
		definitionPath := path.Join(config.Home, "Library", "LaunchAgents", label+".plist")
		return Plan{
			Manager: "launchd", Label: label, DefinitionPath: definitionPath, Content: renderPlist(label, args),
			StartCommands: [][]string{{"/bin/launchctl", "bootstrap", domain, definitionPath}, {"/bin/launchctl", "kickstart", "-p", target}},
			StopCommands:  [][]string{{"/bin/launchctl", "bootout", target}},
		}, nil
	case PlatformLinux:
		configHome := config.ConfigHome
		if configHome == "" {
			configHome = path.Join(config.Home, ".config")
		}
		return Plan{
			Manager: "systemd-user", Label: label,
			DefinitionPath: path.Join(configHome, "systemd", "user", label+".service"), Content: renderUnit(config.BinaryName, args),
			StartCommands: [][]string{{"/usr/bin/systemctl", "--user", "daemon-reload"}, {"/usr/bin/systemctl", "--user", "start", label + ".service"}},
			StopCommands:  [][]string{{"/usr/bin/systemctl", "--user", "stop", label + ".service"}},
		}, nil
	default:
		panic("validated platform")
	}
}

func daemonArgs(config Config) []string {
	args := []string{config.Executable, "daemon", "run", "--state-dir", config.StateDir, "--path", config.Path, "--interval", config.Interval.String()}
	for _, root := range config.Roots {
		args = append(args, "--root", root)
	}
	return args
}

func validatePath(name, value string, maxBytes int) error {
	if value == "" || !utf8.ValidString(value) || strings.IndexFunc(value, invalidPathRune) >= 0 || len(value) > maxBytes || !path.IsAbs(value) || path.Clean(value) != value || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a bounded, clean absolute path without control characters", name)
	}
	return nil
}

func invalidPathRune(r rune) bool { return r < 0x20 || r == 0x7f || r == 0xfffe || r == 0xffff }

func validLabelPart(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			continue
		default:
			return false
		}
	}
	return true
}

func containsRoot(parent, child string) bool {
	if parent == "/" {
		return path.IsAbs(child)
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func renderPlist(label string, args []string) string {
	var out strings.Builder
	out.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&out, "Label", label)
	out.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range args {
		out.WriteString("    <string>")
		_ = xml.EscapeText(&out, []byte(arg))
		out.WriteString("</string>\n")
	}
	out.WriteString("  </array>\n  <key>ProcessType</key>\n  <string>Background</string>\n</dict>\n</plist>\n")
	return out.String()
}

func writePlistString(out *strings.Builder, key, value string) {
	out.WriteString("  <key>")
	_ = xml.EscapeText(out, []byte(key))
	out.WriteString("</key>\n  <string>")
	_ = xml.EscapeText(out, []byte(value))
	out.WriteString("</string>\n")
}

func renderUnit(binaryName string, args []string) string {
	var out strings.Builder
	out.WriteString("[Unit]\nDescription=")
	out.WriteString(binaryName)
	out.WriteString(" observation daemon (manual start)\n\n[Service]\nType=simple\nExecStart=:")
	for i, arg := range args {
		if i > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(systemdArgument(arg))
	}
	out.WriteByte('\n')
	return out.String()
}

// systemdArgument preserves one literal ExecStart argument. Percent escapes
// avoid specifier expansion. The ExecStart colon prefix disables environment
// expansion for the complete argv, including the executable token.
func systemdArgument(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString("\\\\")
		case '"':
			out.WriteString("\\\"")
		case '%':
			out.WriteString("%%")
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}
