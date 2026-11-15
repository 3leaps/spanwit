package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// approvedOnlyRig builds root + prune (+ inventory) with the real flag names.
func approvedOnlyRig(t *testing.T) (*cobra.Command, *cobra.Command, *cobra.Command) {
	t.Helper()
	resetPolicyForTest(t)
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	root.PersistentFlags().Bool(approvedOnlyFlagName, false, "")
	prune := &cobra.Command{Use: "prune", Run: func(*cobra.Command, []string) {}}
	setCommandCapability(prune, CapabilityPlanning)
	prune.Flags().BoolP("execute", "e", false, "")
	prune.Flags().String("approval", "", "")
	prune.Flags().String("from-exact-plan", "", "")
	inv := &cobra.Command{Use: "inventory", Run: func(*cobra.Command, []string) {}}
	setCommandCapability(inv, CapabilityObservational)
	inv.Flags().String("coverage-attest", "", "")
	root.AddCommand(prune, inv)
	return root, prune, inv
}

func TestApprovedOnlyAdmitsOnlyApprovedPrune(t *testing.T) {
	_, prune, inv := approvedOnlyRig(t)
	t.Setenv("SPANWIT_APPROVED_ONLY", "1")

	// plain --execute: refused
	_ = prune.Flags().Set("execute", "true")
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "approved-only") {
		t.Fatalf("plain execute: %v", err)
	}
	// --approval without --from-exact-plan: refused
	_ = prune.Flags().Set("approval", "a-1")
	if err := enforceMutationPolicy(prune); err == nil {
		t.Fatal("approval without exact plan admitted")
	}
	// the one admitted shape
	_ = prune.Flags().Set("from-exact-plan", "/p.json")
	if err := enforceMutationPolicy(prune); err != nil {
		t.Fatalf("approved prune refused: %v", err)
	}
	if !ResolvedApprovedOnly() || assertionLabel() != "approval_bound" {
		t.Fatalf("approved prune not labelled approval_bound")
	}
	// observational commands still run, labelled read_only (they can mutate nothing)
	if err := enforceMutationPolicy(inv); err != nil {
		t.Fatalf("inventory refused: %v", err)
	}
	if ResolvedMutationContract() != MutationContractReadOnly {
		t.Fatalf("contract %s", ResolvedMutationContract())
	}
	// a mutating mode of an observational command is refused
	_ = inv.Flags().Set("coverage-attest", "/x")
	if err := enforceMutationPolicy(inv); err == nil {
		t.Fatal("coverage-attest admitted under approved-only")
	}
}

func TestApprovedOnlyConflictsWithReadOnly(t *testing.T) {
	_, prune, _ := approvedOnlyRig(t)
	t.Setenv("SPANWIT_APPROVED_ONLY", "1")
	t.Setenv("SPANWIT_READ_ONLY", "1")
	_ = prune.Flags().Set("execute", "true")
	_ = prune.Flags().Set("approval", "a-1")
	_ = prune.Flags().Set("from-exact-plan", "/p.json")
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "conflicting assertions") {
		t.Fatalf("both assertions: %v", err)
	}
}

func TestReadOnlyStillRefusesApprovedPrune(t *testing.T) {
	_, prune, _ := approvedOnlyRig(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	_ = prune.Flags().Set("execute", "true")
	_ = prune.Flags().Set("approval", "a-1")
	_ = prune.Flags().Set("from-exact-plan", "/p.json")
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("read_only must refuse an approved prune too: %v", err)
	}
}

func TestApprovedOnlyFlagEnvConflictFailsClosed(t *testing.T) {
	root, prune, _ := approvedOnlyRig(t)
	t.Setenv("SPANWIT_APPROVED_ONLY", "0")
	if err := root.PersistentFlags().Set(approvedOnlyFlagName, "true"); err != nil {
		t.Fatal(err)
	}
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("flag/env conflict: %v", err)
	}
}
