package reportmodel

// Recipe kind: argv recipes are single-shot external command suggestions;
// structural recipes are multi-step guided journeys (print-only).
const (
	RecipeKindArgv       = "argv"
	RecipeKindStructural = "structural"
)

// Recipe step kinds for structural journeys. None are auto-executed.
const (
	StepObserve       = "observe"
	StepCommand       = "command"
	StepConfigSnippet = "config_snippet"
	StepPurgeHint     = "purge_hint"
	StepVerify        = "verify"
)

// RecipeStep is one ordered step in a structural guided recipe.
// Print-only: spanwit never writes config, never moves trees, never deletes.
type RecipeStep struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Kind  string   `json:"kind"`
	Body  []string `json:"body,omitempty"`
	Paths []string `json:"paths,omitempty"`
	// Command is set for kind=command (or purge_hint when a revalidate argv exists).
	Command *SuggestedCommand `json:"command,omitempty"`
	// ConfigSnippet is set for kind=config_snippet — copy-paste only.
	ConfigSnippet *ConfigSnippet `json:"config_snippet,omitempty"`
}

// ConfigSnippet is a copy-paste config fragment (never written by spanwit).
type ConfigSnippet struct {
	// Label is a human file/role hint (e.g. ".envrc", "shell profile").
	Label   string `json:"label"`
	Content string `json:"content"`
}

// ProjectCommandsFromSteps returns argv suggestions in step order (structural projection).
func ProjectCommandsFromSteps(steps []RecipeStep) []SuggestedCommand {
	out := make([]SuggestedCommand, 0)
	for _, s := range steps {
		if s.Command != nil {
			out = append(out, *s.Command)
		}
	}
	return out
}
