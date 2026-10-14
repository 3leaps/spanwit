// Package envelope holds the fields every whole-document spanwit report
// carries: the producing tool, whether the capture completed, and a summary
// of what the document discloses. It is a leaf package so capacity, space,
// observe and cmd share one definition and one disclosure rule.
package envelope

import "sync"

// Tool identifies the spanwit build that produced a document. It is exactly
// name, version and an optional commit: never a host, user, cwd or path.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

// UnknownTool is what a loader reports for a document that carries no tool
// object (written before v0.2.2). It is never a filled-in guess.
var UnknownTool = Tool{Name: "unknown", Version: "unknown"}

var (
	toolMu  sync.RWMutex
	current = Tool{Name: "spanwit", Version: "dev"}
)

// SetTool records the running build's identity (called once from main via
// cmd.SetVersionInfo).
func SetTool(t Tool) {
	toolMu.Lock()
	defer toolMu.Unlock()
	current = t
}

// CurrentTool returns a copy of the running build's identity for stamping.
func CurrentTool() *Tool {
	toolMu.RLock()
	defer toolMu.RUnlock()
	t := current
	return &t
}

// ToolOf returns the carried tool, or UnknownTool when the document has none.
func ToolOf(t *Tool) Tool {
	if t == nil {
		return UnknownTool
	}
	return *t
}

// Completion lifecycles. There is no failed value: a failed capture exits 2
// and emits no document.
const (
	CompletionComplete = "complete"
	CompletionPartial  = "partial"
)

// Completion is the validity of a capture, independent of the process exit
// code: partial iff warning_count > 0. A partial document is still usable.
type Completion struct {
	Lifecycle    string `json:"lifecycle"`
	WarningCount int    `json:"warning_count"`
}

// NewCompletion derives the completion record from a warning count.
func NewCompletion(warnings int) *Completion {
	c := &Completion{Lifecycle: CompletionComplete, WarningCount: warnings}
	if warnings > 0 {
		c.Lifecycle = CompletionPartial
	}
	return c
}
