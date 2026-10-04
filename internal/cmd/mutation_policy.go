package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/inventory"
)

// Command mutation capability classes. Registration is declarative;
// resolved capability may be elevated by mode flags (e.g. prune --execute,
// inventory --coverage-attest).
const (
	CapabilityObservational = "observational"
	CapabilityPlanning      = "planning"
	CapabilityMutating      = "mutating"

	annotationCapability = "spanwit.capability"

	// Flag and env names for the global no-mutation assertion.
	readOnlyFlagName = "read-only"
	// envReadOnlySuffix is appended to identity EnvPrefix (e.g. SPANWIT_READ_ONLY).
	envReadOnlySuffix = "READ_ONLY"
)

// Mutation contract labels are inventory SSOT (open | read_only).
const (
	MutationContractOpen     = inventory.MutationContractOpen
	MutationContractReadOnly = inventory.MutationContractReadOnly
)

// mutationEnvPrefix is set during Initialize from app identity (e.g. "SPANWIT_").
var mutationEnvPrefix string

// assertReadOnlyFlag is bound to the root --read-only persistent flag.
var assertReadOnlyFlag bool

// setCommandCapability records the base capability on a cobra command.
// Only the closed enum is accepted (fail-closed at registration).
func setCommandCapability(cmd *cobra.Command, capability string) {
	switch capability {
	case CapabilityObservational, CapabilityPlanning, CapabilityMutating:
	default:
		panic(fmt.Sprintf("invalid mutation capability %q (use observational|planning|mutating)", capability))
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationCapability] = capability
}

func commandCapability(cmd *cobra.Command) string {
	if cmd == nil || cmd.Annotations == nil {
		return ""
	}
	return cmd.Annotations[annotationCapability]
}

// resolveInvocationCapability returns the effective capability for this run.
// Mode flags may elevate a declared base capability to mutating. Unannotated
// commands resolve to empty (fail-closed under assertion) — there is no
// silent name-based planning default.
func resolveInvocationCapability(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	// Mode elevations first (mutating wins when the flag is active).
	switch cmd.Name() {
	case "prune":
		if f := cmd.Flags().Lookup("execute"); f != nil {
			if v, err := cmd.Flags().GetBool("execute"); err == nil && v {
				return CapabilityMutating
			}
		}
	case "inventory":
		// Durable coverage-attestation publication is a filesystem mutation.
		if f := cmd.Flags().Lookup("coverage-attest"); f != nil {
			if v, err := cmd.Flags().GetString("coverage-attest"); err == nil && strings.TrimSpace(v) != "" {
				return CapabilityMutating
			}
		}
	}
	return commandCapability(cmd)
}

// parseTruthyEnv interprets common affirmative/negative env spellings.
// ok is false when the value is empty/unset.
func parseTruthyEnv(raw string) (on bool, set bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, false, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true, true, nil
	case "0", "false", "no", "n", "off":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("invalid %s value %q (use true|false|1|0|yes|no|y|n|on|off)", envReadOnlySuffix, raw)
	}
}

// resolveReadOnlyAssertion combines flag and env. Explicit flag/env conflict
// fails closed. Either source alone may assert.
func resolveReadOnlyAssertion(flagChanged bool, flagVal bool, envRaw string) (asserted bool, err error) {
	envOn, envSet, err := parseTruthyEnv(envRaw)
	if err != nil {
		return false, err
	}
	if flagChanged && envSet && flagVal != envOn {
		return false, fmt.Errorf("conflicting read-only assertion: --%s=%v vs %s%s=%q (fail-closed)",
			readOnlyFlagName, flagVal, mutationEnvPrefix, envReadOnlySuffix, envRaw)
	}
	if flagChanged {
		return flagVal, nil
	}
	if envSet {
		return envOn, nil
	}
	return false, nil
}

// effectiveMutationContract returns the header/report contract label.
func effectiveMutationContract(asserted bool) string {
	if asserted {
		return MutationContractReadOnly
	}
	return MutationContractOpen
}

// isMutationPolicyExempt skips only known unannotated Cobra shell surfaces
// (help / completion / live-complete helpers). Any capability annotation on the
// leaf-to-root chain blocks exemption so a product mutator cannot hide under a
// shell-shaped name. There is no broad hidden-name fallback.
func isMutationPolicyExempt(cmd *cobra.Command) bool {
	if cmd == nil {
		return true
	}
	// Bare product root (help-only) — not a leaf work command.
	if cmd.Parent() == nil {
		return true
	}
	// Full chain: any product capability annotation ⇒ never exempt.
	for c := cmd; c != nil; c = c.Parent() {
		if commandCapability(c) != "" {
			return false
		}
	}
	// Exact known unannotated shell surfaces only (leaf or ancestor).
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "help", "completion", "__complete", "__completeNoDesc":
			return true
		}
		if c.Parent() == nil {
			break
		}
	}
	return false
}

// enforceMutationPolicy rejects asserted read-only invocations that resolve to
// mutating or unknown capability. Call after flags are parsed, before discovery/work.
func enforceMutationPolicy(cmd *cobra.Command) error {
	// Always reset process-local resolved state at the start of enforce so a
	// prior invocation cannot leak contract/capability into this run.
	setResolvedMutationState(false, "")

	if cmd == nil || isMutationPolicyExempt(cmd) {
		return nil
	}

	envKey := mutationEnvPrefix + envReadOnlySuffix
	flagChanged := false
	flagVal := assertReadOnlyFlag
	if root := cmd.Root(); root != nil {
		if f := root.PersistentFlags().Lookup(readOnlyFlagName); f != nil {
			flagChanged = f.Changed
			if v, err := root.PersistentFlags().GetBool(readOnlyFlagName); err == nil {
				flagVal = v
			}
		}
	}
	asserted, err := resolveReadOnlyAssertion(flagChanged, flagVal, os.Getenv(envKey))
	if err != nil {
		return err
	}
	cap := resolveInvocationCapability(cmd)
	// Stash for subcommands (inventory/space headers).
	setResolvedMutationState(asserted, cap)

	if !asserted {
		return nil
	}

	// Under assertion: only exact observational|planning are allowed.
	// Empty, typo, mutating, or any other value fails closed.
	switch cap {
	case CapabilityObservational, CapabilityPlanning:
		return nil
	case CapabilityMutating:
		return fmt.Errorf("read-only assertion forbids mutating capability (command %q resolved to %s; remove --execute/--coverage-attest or unset --%s / %s)",
			cmd.CommandPath(), cap, readOnlyFlagName, envKey)
	default:
		if cap == "" {
			return fmt.Errorf("read-only assertion active but command %q has no declared mutation capability (fail-closed)", cmd.CommandPath())
		}
		return fmt.Errorf("read-only assertion active but command %q has unknown mutation capability %q (fail-closed; allow only observational|planning)",
			cmd.CommandPath(), cap)
	}
}

// resolved mutation state for this process invocation (set by enforce).
var (
	resolvedReadOnlyAsserted bool
	resolvedCapability       string
)

func setResolvedMutationState(asserted bool, capability string) {
	resolvedReadOnlyAsserted = asserted
	resolvedCapability = capability
}

func resetMutationPolicyState() {
	assertReadOnlyFlag = false
	setResolvedMutationState(false, "")
}

// ResolvedMutationContract is the effective contract for headers/reports.
func ResolvedMutationContract() string {
	return effectiveMutationContract(resolvedReadOnlyAsserted)
}

// ResolvedReadOnlyAsserted reports whether the no-mutation assertion is active.
func ResolvedReadOnlyAsserted() bool {
	return resolvedReadOnlyAsserted
}

// ResolvedCapability is the leaf capability after mode elevation.
func ResolvedCapability() string {
	return resolvedCapability
}
