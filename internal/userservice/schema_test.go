//go:build darwin || linux

package userservice

import (
	"encoding/json"
	"testing"
	"time"

	schema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestPreviewContractRefusesEffectClaims(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		plan, err := Render(Config{Platform: platform, Executable: "/opt/bin/example", Home: "/home/example", StateDir: "/home/example/state", Path: "/home/example", Interval: time.Minute, BinaryName: "example", Vendor: "vendor", UID: 1000})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.ValidateJSON(schema.SpanwitUserServicePlanV0, raw); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"definition_written", "commands_executed"} {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			value[key] = true
			bad, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if contract.ValidateJSON(schema.SpanwitUserServicePlanV0, bad) == nil {
				t.Fatalf("accepted %s claim", key)
			}
		}
	}
}

func TestStatusReportSchemaEnforcesHealthAndActionInvariants(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validHealthy := Report{
		Schema:       StatusSchema,
		Version:      0,
		Action:       "status",
		Outcome:      "observed",
		Uncertain:    false,
		Healthy:      true,
		OverallState: "healthy",
		Installation: InstallationSummary{
			State:                   "installed",
			Manager:                 "launchd",
			Label:                   "vendor.example.daemon",
			StateDir:                "/home/example/state",
			DefinitionPath:          "/home/example/Library/LaunchAgents/vendor.example.daemon.plist",
			ExecutablePath:          "/opt/bin/example",
			SamplePath:              "/home/example",
			Interval:                "5m0s",
			ConfigSHA256:            hash,
			DefinitionSHA256:        hash,
			ExecutableSHA256:        hash,
			CurrentExecutableSHA256: hash,
			OnDiskDefinitionMatches: true,
			OnDiskExecutableMatches: true,
		},
		Manager: ManagerSummary{
			Manager:  "launchd",
			Label:    "vendor.example.daemon",
			State:    "running",
			PID:      4242,
			SubState: "running",
		},
		Daemon: DaemonSummary{
			SocketPresent:            true,
			Reachable:                true,
			State:                    "observing",
			InstanceID:               "a3df2fb7-6b94-4fef-8b7b-3b4283cc2393",
			PID:                      4242,
			StartedAt:                "2026-11-13T12:00:00Z",
			ExecutablePath:           "/opt/bin/example",
			ExecutableSHA256:         hash,
			ConfigSHA256:             hash,
			PIDMatchesManager:        true,
			ConfigMatchesReceipt:     true,
			ExecutableMatchesReceipt: true,
			ExecutableMatchesDisk:    true,
		},
	}
	raw, err := json.Marshal(validHealthy)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(schema.SpanwitUserServiceStatusV0, raw); err != nil {
		t.Fatalf("rejected valid healthy report: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Report)
	}{
		{"healthy-without-pid-match", func(r *Report) { r.Daemon.PIDMatchesManager = false }},
		{"healthy-without-config-match", func(r *Report) { r.Daemon.ConfigMatchesReceipt = false }},
		{"healthy-without-exec-disk-match", func(r *Report) { r.Daemon.ExecutableMatchesDisk = false }},
		{"healthy-while-uncertain", func(r *Report) { r.Uncertain = true }},
		{"unconfirmed-not-uncertain", func(r *Report) {
			r.Healthy = false
			r.OverallState = "unknown"
			r.Outcome = "unconfirmed"
			r.Uncertain = false
		}},
		{"status-claimed-commands", func(r *Report) { r.CommandsExecuted = true }},
		{"install-claimed-commands", func(r *Report) {
			r.Action = "install"
			r.Outcome = "installed"
			r.CommandsExecuted = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyReport := validHealthy
			tc.mutate(&copyReport)
			bad, err := json.Marshal(copyReport)
			if err != nil {
				t.Fatal(err)
			}
			if contract.ValidateJSON(schema.SpanwitUserServiceStatusV0, bad) == nil {
				t.Fatalf("accepted invalid status report %s", tc.name)
			}
		})
	}
}
