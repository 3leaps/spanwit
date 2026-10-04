package space

import (
	"os"
	"path/filepath"
	"strings"
)

// PlacementNotes returns advisory notes about relocating Cargo output when the
// primary write volume is under pressure. Never mutates env or creates links.
// Env-derived paths are quoted safely if ever emitted as commands; this first
// slice emits prose notes only (no shell interpolation of untrusted env).
func PlacementNotes(pressure Pressure, analysis *Pressure) []string {
	level := pressure.Level
	if level != PressureCritical && level != PressureWarn {
		return nil
	}

	notes := []string{
		"Placement (advisory): when the primary write volume is tight, prefer project-specific CARGO_TARGET_DIR on a roomier volume rather than one global shared target dir.",
		"Placement caveat: a relocated CARGO_TARGET_DIR outside a Cargo.toml ancestor is not context-verified by the built-in cargo-target signature — treat as diagnostic/manual unless an explicit provenance contract is added later.",
		"Placement is advice-only: spanwit never moves trees, never creates symlinks, and never mutates Cargo config.",
	}

	// Prefer measured analysis_pressure when it proves a distinct, roomier volume.
	if analysis != nil && analysis.AvailBytes > pressure.AvailBytes &&
		analysis.VolumeID != "" && pressure.VolumeID != "" &&
		analysis.VolumeID != pressure.VolumeID {
		notes = append(notes,
			"Analysis root volume has more free space than the primary write volume; consider project-specific target dirs on the analysis volume when safe for your workflow.")
	}

	// Optional: mention env only as inert data (never in a shell string).
	if env := strings.TrimSpace(os.Getenv("CARGO_TARGET_DIR")); env != "" {
		// Do not interpolate into commands; report presence only.
		if ContainsUnsafeShellMeta(env) || strings.ContainsAny(env, " \t\n") {
			notes = append(notes,
				"CARGO_TARGET_DIR is set but contains characters unsafe for shell suggestions; not emitting a command (inspect env manually).")
		} else {
			// Still no command — path may lack Cargo.toml ancestor.
			clean := filepath.Clean(env)
			notes = append(notes,
				"CARGO_TARGET_DIR is set to "+ShellQuote(clean)+" (not auto-managed by spanwit; signature verification still requires a Cargo.toml ancestor).")
		}
	}

	return notes
}
