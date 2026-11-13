//go:build darwin || linux

package userservice

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(platform string) Config {
	return Config{
		Platform: platform, Executable: "/opt/spanwit/bin/spanwit", Home: "/Users/tester",
		StateDir: "/Users/tester/.local/state/spanwit/daemon", Path: "/Users/tester",
		Roots: []string{"/Users/tester/Downloads", "/Users/tester/dev/temp"}, Interval: 5 * time.Minute,
		BinaryName: "spanwit", Vendor: "3leaps", UID: 501,
	}
}

func TestRenderDarwinManualStartPlan(t *testing.T) {
	plan, err := Render(fixture(PlatformDarwin))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manager != "launchd" || plan.Label != "3leaps.spanwit.daemon" || plan.DefinitionPath != "/Users/tester/Library/LaunchAgents/3leaps.spanwit.daemon.plist" || plan.DefinitionWritten || plan.CommandsExecuted {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if !strings.Contains(plan.Content, "<key>ProgramArguments</key>") || strings.Contains(plan.Content, "RunAtLoad") || strings.Contains(plan.Content, "KeepAlive") {
		t.Fatalf("not manual-start plist:\n%s", plan.Content)
	}
	bootstrap := []string{"/bin/launchctl", "bootstrap", "gui/501", "/Users/tester/Library/LaunchAgents/3leaps.spanwit.daemon.plist"}
	start := []string{"/bin/launchctl", "kickstart", "-p", "gui/501/3leaps.spanwit.daemon"}
	stop := []string{"/bin/launchctl", "bootout", "gui/501/3leaps.spanwit.daemon"}
	if !same(plan.StartCommands[0], bootstrap) || !same(plan.StartCommands[1], start) || !same(plan.StopCommands[0], stop) {
		t.Fatalf("unexpected argv plan: %+v", plan)
	}
}

func TestRenderLinuxEscapesUnitArguments(t *testing.T) {
	config := fixture(PlatformLinux)
	config.Executable = "/opt/Spanwit $%/spanwit"
	config.Roots = []string{"/Users/tester/Downloads space", "/Users/tester/dev/$value%"}
	plan, err := Render(config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manager != "systemd-user" || plan.DefinitionPath != "/Users/tester/.config/systemd/user/3leaps.spanwit.daemon.service" || plan.DefinitionWritten || plan.CommandsExecuted {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if strings.Contains(plan.Content, "Restart=") || strings.Contains(plan.Content, "[Install]") {
		t.Fatalf("unexpected activation policy:\n%s", plan.Content)
	}
	for _, want := range []string{"ExecStart=:\"/opt/Spanwit $%%/spanwit\"", "\"/Users/tester/Downloads space\"", "\"/Users/tester/dev/$value%%\""} {
		if !strings.Contains(plan.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, plan.Content)
		}
	}
	if got := plan.StartCommands; len(got) != 2 || !same(got[0], []string{"/usr/bin/systemctl", "--user", "daemon-reload"}) || !same(got[1], []string{"/usr/bin/systemctl", "--user", "start", "3leaps.spanwit.daemon.service"}) {
		t.Fatalf("unexpected start commands: %q", got)
	}

}

func TestValidateRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Platform = "windows" }, func(c *Config) { c.UID = -1 },
		func(c *Config) { c.Executable = "spanwit" }, func(c *Config) { c.StateDir += "/." },
		func(c *Config) { c.Path += "\n" }, func(c *Config) { c.Interval = time.Second },
		func(c *Config) { c.Vendor = "3leaps;rm" }, func(c *Config) { c.Roots = []string{"/a", "/a/b"} },
		func(c *Config) { c.Roots = []string{"/a "} },
	}
	for _, edit := range cases {
		config := fixture(PlatformDarwin)
		edit(&config)
		if err := Validate(config); err == nil {
			t.Fatalf("accepted unsafe config: %+v", config)
		}
	}
}

func TestExecutablePathRestrictionsArePlatformSpecific(t *testing.T) {
	for _, executable := range []string{`/opt/quote"/program`, `/opt/quote'/program`, `/opt/back\slash/program`} {
		config := fixture(PlatformLinux)
		config.Executable = executable
		if _, err := Render(config); err == nil {
			t.Fatalf("accepted unsupported systemd executable %q", executable)
		}
		config.Platform = PlatformDarwin
		if _, err := Render(config); err != nil {
			t.Fatalf("rejected launchd executable %q: %v", executable, err)
		}
		config = fixture(PlatformLinux)
		config.StateDir = executable
		config.Roots = []string{executable}
		if _, err := Render(config); err != nil {
			t.Fatalf("rejected ordinary systemd argument %q: %v", executable, err)
		}
	}
}

func TestRenderDoesNotUseHostState(t *testing.T) {
	config := fixture(PlatformLinux)
	first, err := Render(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("render result changed for identical input")
	}
}

func same(left, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func TestLinuxConfigHome(t *testing.T) {
	c := fixture(PlatformLinux)
	c.ConfigHome = "/custom/user-config"
	plan, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DefinitionPath != "/custom/user-config/systemd/user/3leaps.spanwit.daemon.service" {
		t.Fatal(plan.DefinitionPath)
	}
	c.ConfigHome = "relative"
	if _, err := Render(c); err == nil {
		t.Fatal("accepted relative config home")
	}
}
