package space

import (
	"regexp"
	"strings"
	"unicode"
)

// SuggestedCommand is a machine-safe external command suggestion (argv form).
// Display is a shell-safe single-line rendering for text mode. Recipes and
// handoff never auto-execute these commands.
type SuggestedCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Display string   `json:"display"`
}

// safeTokenPattern allows only characters safe for rustup toolchain identifiers
// and similar tool args (no shell metacharacters).
var safeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// IsSafeToken reports whether s is free of shell metacharacters and whitespace.
func IsSafeToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	return safeTokenPattern.MatchString(s)
}

// ShellQuote returns a POSIX-shell-safe single-quoted form of s.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Prefer bare token when already safe.
	if IsSafeToken(s) && !strings.ContainsAny(s, " \t\n") {
		// Paths often need quoting; only skip for simple flags/programs.
		if !strings.Contains(s, "/") && !strings.Contains(s, "\\") {
			return s
		}
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// FormatCommand builds a SuggestedCommand with argv and a shell-safe Display line.
func FormatCommand(program string, args ...string) SuggestedCommand {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, ShellQuote(program))
	for _, a := range args {
		parts = append(parts, ShellQuote(a))
	}
	return SuggestedCommand{
		Program: program,
		Args:    append([]string(nil), args...),
		Display: strings.Join(parts, " "),
	}
}

// ContainsUnsafeShellMeta reports whether s contains characters that would be
// dangerous if interpolated into a shell without quoting.
func ContainsUnsafeShellMeta(s string) bool {
	for _, r := range s {
		switch r {
		case ';', '&', '|', '$', '`', '(', ')', '<', '>', '\n', '\r', '\t', ' ',
			'"', '\'', '\\', '!', '*', '?', '[', ']', '{', '}', '~':
			return true
		default:
			if !unicode.IsPrint(r) {
				return true
			}
		}
	}
	return false
}
