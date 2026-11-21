package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/engine"
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

	// Flag and env names for the approval-bound assertion: the invocation may
	// mutate only through prune --from-exact-plan --execute --approval.
	approvedOnlyFlagName  = "approved-only"
	envApprovedOnlySuffix = "APPROVED_ONLY"
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

// assertApprovedOnlyFlag is bound to the root --approved-only persistent flag.
var assertApprovedOnlyFlag bool

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
	return resolveAssertion(readOnlyFlagName, envReadOnlySuffix, flagChanged, flagVal, envRaw)
}

func resolveAssertion(flagName, envSuffix string, flagChanged bool, flagVal bool, envRaw string) (asserted bool, err error) {
	envOn, envSet, err := parseTruthyEnv(envRaw)
	if err != nil {
		return false, err
	}
	if flagChanged && envSet && flagVal != envOn {
		return false, fmt.Errorf("conflicting %s assertion: --%s=%v vs %s%s=%q (fail-closed)",
			flagName, flagName, flagVal, mutationEnvPrefix, envSuffix, envRaw)
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
	resolvedApprovedOnly = false

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
	approvedOnly, err := resolveApprovedOnly(cmd)
	if err != nil {
		return err
	}
	if asserted && approvedOnly {
		return fmt.Errorf("conflicting assertions: --%s and --%s cannot both be active (fail-closed)", readOnlyFlagName, approvedOnlyFlagName)
	}
	cap := resolveInvocationCapability(cmd)
	// Stash for subcommands (inventory/space headers). Under approved-only a
	// non-approved invocation can mutate nothing, so its reports carry the
	// read_only contract; an approved prune reports approval_bound itself.
	setResolvedMutationState(asserted || approvedOnly, cap)
	resolvedApprovedOnly = approvedOnly

	if approvedOnly {
		if cap == CapabilityMutating && isApprovedPruneInvocation(cmd) {
			return nil
		}
		asserted = true
	}
	if !asserted {
		return nil
	}

	// Under assertion: only exact observational|planning are allowed.
	// Empty, typo, mutating, or any other value fails closed.
	switch cap {
	case CapabilityObservational, CapabilityPlanning:
		return nil
	case CapabilityMutating:
		if approvedOnly {
			return fmt.Errorf("approved-only assertion admits only `prune --from-exact-plan <plan> --execute --approval <id>` (command %q resolved to %s; unset --%s / %s%s)",
				cmd.CommandPath(), cap, approvedOnlyFlagName, mutationEnvPrefix, envApprovedOnlySuffix)
		}
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
	resolvedApprovedOnly     bool
)

// ResolvedApprovedOnly reports whether the approval-bound assertion is active.
func ResolvedApprovedOnly() bool {
	return resolvedApprovedOnly
}

func resolveApprovedOnly(cmd *cobra.Command) (bool, error) {
	flagChanged, flagVal := false, assertApprovedOnlyFlag
	if root := cmd.Root(); root != nil {
		if f := root.PersistentFlags().Lookup(approvedOnlyFlagName); f != nil {
			flagChanged = f.Changed
			if v, err := root.PersistentFlags().GetBool(approvedOnlyFlagName); err == nil {
				flagVal = v
			}
		}
	}
	return resolveAssertion(approvedOnlyFlagName, envApprovedOnlySuffix, flagChanged, flagVal, os.Getenv(mutationEnvPrefix+envApprovedOnlySuffix))
}

// isApprovedPruneInvocation is the single shape approved-only admits:
// prune with --execute, a non-empty --approval and --from-exact-plan.
func isApprovedPruneInvocation(cmd *cobra.Command) bool {
	if cmd.Name() != "prune" || cmd.Parent() == nil || cmd.Parent().Parent() != nil {
		return false
	}
	execute, err1 := cmd.Flags().GetBool("execute")
	approvalID, err2 := cmd.Flags().GetString("approval")
	plan, err3 := cmd.Flags().GetString("from-exact-plan")
	return err1 == nil && err2 == nil && err3 == nil && execute && strings.TrimSpace(approvalID) != "" && strings.TrimSpace(plan) != ""
}

func setResolvedMutationState(asserted bool, capability string) {
	resolvedReadOnlyAsserted = asserted
	resolvedCapability = capability
}

func resetMutationPolicyState() {
	assertReadOnlyFlag = false
	assertApprovedOnlyFlag = false
	resolvedApprovedOnly = false
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

// assertionLabel is the mutation assertion recorded with an execution.
func assertionLabel() string {
	if ResolvedApprovedOnly() {
		return engine.ExecutionAssertionApprovalBound
	}
	return engine.ExecutionAssertionOpen
}
