package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
	"github.com/spf13/cobra"
)

func resetPolicyForTest(t *testing.T) {
	t.Helper()
	mutationEnvPrefix = "SPANWIT_"
	t.Setenv("SPANWIT_READ_ONLY", "")
	resetMutationPolicyState()
	t.Cleanup(func() {
		t.Setenv("SPANWIT_READ_ONLY", "")
		resetMutationPolicyState()
	})
}

func TestParseTruthyEnv(t *testing.T) {
	cases := []struct {
		in      string
		on, set bool
		err     bool
	}{
		{"", false, false, false},
		{"true", true, true, false},
		{"1", true, true, false},
		{"YES", true, true, false},
		{"on", true, true, false},
		{"y", true, true, false},
		{"false", false, true, false},
		{"0", false, true, false},
		{"off", false, true, false},
		{"no", false, true, false},
		{"maybe", false, true, true},
	}
	for _, tc := range cases {
		on, set, err := parseTruthyEnv(tc.in)
		if (err != nil) != tc.err || on != tc.on || set != tc.set {
			t.Fatalf("in=%q got on=%v set=%v err=%v want on=%v set=%v err=%v",
				tc.in, on, set, err != nil, tc.on, tc.set, tc.err)
		}
	}
}

func TestResolveReadOnlyAssertion_ConflictFailClosed(t *testing.T) {
	mutationEnvPrefix = "SPANWIT_"
	_, err := resolveReadOnlyAssertion(true, true, "false")
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("want conflict error, got %v", err)
	}
	// Explicit --read-only=false vs env true is also a conflict.
	_, err = resolveReadOnlyAssertion(true, false, "true")
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("want conflict for flag false vs env true, got %v", err)
	}
	on, err := resolveReadOnlyAssertion(true, true, "true")
	if err != nil || !on {
		t.Fatalf("agreeing true: on=%v err=%v", on, err)
	}
	on, err = resolveReadOnlyAssertion(false, false, "true")
	if err != nil || !on {
		t.Fatalf("env alone: on=%v err=%v", on, err)
	}
	on, err = resolveReadOnlyAssertion(true, false, "")
	if err != nil || on {
		t.Fatalf("flag false alone: on=%v err=%v", on, err)
	}
}

func TestResolveInvocationCapability_PruneExecute(t *testing.T) {
	root := &cobra.Command{Use: "spanwit"}
	prune := &cobra.Command{Use: "prune"}
	setCommandCapability(prune, CapabilityPlanning)
	var execute bool
	prune.Flags().BoolVarP(&execute, "execute", "e", false, "")
	root.AddCommand(prune)

	if got := resolveInvocationCapability(prune); got != CapabilityPlanning {
		t.Fatalf("dry-run prune: %s", got)
	}
	if err := prune.Flags().Set("execute", "true"); err != nil {
		t.Fatal(err)
	}
	if got := resolveInvocationCapability(prune); got != CapabilityMutating {
		t.Fatalf("execute prune: %s", got)
	}
}

func TestResolveInvocationCapability_InventoryCoverageAttestIsMutating(t *testing.T) {
	inv := &cobra.Command{Use: "inventory"}
	setCommandCapability(inv, CapabilityObservational)
	var path string
	inv.Flags().StringVar(&path, "coverage-attest", "", "")
	if got := resolveInvocationCapability(inv); got != CapabilityObservational {
		t.Fatalf("plain inventory: %s", got)
	}
	if err := inv.Flags().Set("coverage-attest", "/tmp/attest.json"); err != nil {
		t.Fatal(err)
	}
	if got := resolveInvocationCapability(inv); got != CapabilityMutating {
		t.Fatalf("coverage-attest must elevate to mutating, got %s", got)
	}
}

func TestResolveInvocationCapability_UnannotatedPruneIsEmpty(t *testing.T) {
	// No silent planning default for unannotated prune.
	prune := &cobra.Command{Use: "prune"}
	var execute bool
	prune.Flags().BoolVar(&execute, "execute", false, "")
	if got := resolveInvocationCapability(prune); got != "" {
		t.Fatalf("unannotated prune dry-run must be empty (fail-closed), got %q", got)
	}
}

func TestIsMutationPolicyExempt(t *testing.T) {
	root := &cobra.Command{Use: "spanwit"}
	if !isMutationPolicyExempt(root) {
		t.Fatal("bare root must be exempt")
	}
	help := &cobra.Command{Use: "help"}
	root.AddCommand(help)
	if !isMutationPolicyExempt(help) {
		t.Fatal("help must be exempt")
	}
	complete := &cobra.Command{Use: "__complete", Hidden: true}
	root.AddCommand(complete)
	if !isMutationPolicyExempt(complete) {
		t.Fatal("__complete must be exempt")
	}
	// Generated completion leaves (completion bash) exempt by ancestry.
	comp := &cobra.Command{Use: "completion"}
	bash := &cobra.Command{Use: "bash"}
	comp.AddCommand(bash)
	root.AddCommand(comp)
	if !isMutationPolicyExempt(bash) {
		t.Fatal("completion bash leaf must be exempt by ancestry")
	}
	prune := &cobra.Command{Use: "prune"}
	setCommandCapability(prune, CapabilityPlanning)
	root.AddCommand(prune)
	if isMutationPolicyExempt(prune) {
		t.Fatal("annotated prune must not be exempt")
	}
}

func TestIsMutationPolicyExempt_AnnotatedCompletionParentNotExempt(t *testing.T) {
	// Annotated mutating command named "completion" must not exempt children.
	root := &cobra.Command{Use: "spanwit"}
	comp := &cobra.Command{Use: "completion"}
	setCommandCapability(comp, CapabilityMutating)
	bash := &cobra.Command{Use: "bash", Run: func(cmd *cobra.Command, args []string) {}}
	comp.AddCommand(bash)
	root.AddCommand(comp)
	if isMutationPolicyExempt(bash) {
		t.Fatal("unannotated child of annotated completion mutator must NOT be exempt")
	}
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	if err := enforceMutationPolicy(bash); err == nil {
		t.Fatal("must fail closed under RO for annotated-completion child")
	}
}

func TestIsMutationPolicyExempt_HiddenPrefixNotEnough(t *testing.T) {
	root := &cobra.Command{Use: "spanwit"}
	mut := &cobra.Command{Use: "__mutator", Hidden: true, Run: func(cmd *cobra.Command, args []string) {}}
	root.AddCommand(mut)
	if isMutationPolicyExempt(mut) {
		t.Fatal("hidden __* prefix alone must not exempt")
	}
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	if err := enforceMutationPolicy(mut); err == nil || !strings.Contains(err.Error(), "no declared") {
		t.Fatalf("hidden unannotated mutator under RO must fail closed, got %v", err)
	}
}

func TestEnforceMutationPolicy_BlocksExecuteWhenReadOnly(t *testing.T) {
	resetPolicyForTest(t)

	root := &cobra.Command{Use: "spanwit"}
	var ro bool
	root.PersistentFlags().BoolVar(&ro, readOnlyFlagName, false, "")
	assertReadOnlyFlag = false

	prune := &cobra.Command{Use: "prune", Run: func(cmd *cobra.Command, args []string) {}}
	setCommandCapability(prune, CapabilityPlanning)
	var execute bool
	prune.Flags().BoolVarP(&execute, "execute", "e", false, "")
	root.AddCommand(prune)

	if err := root.PersistentFlags().Set(readOnlyFlagName, "true"); err != nil {
		t.Fatal(err)
	}
	assertReadOnlyFlag = true
	if err := prune.Flags().Set("execute", "true"); err != nil {
		t.Fatal(err)
	}
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want mutating rejection, got %v", err)
	}

	if err := prune.Flags().Set("execute", "false"); err != nil {
		t.Fatal(err)
	}
	if err := enforceMutationPolicy(prune); err != nil {
		t.Fatalf("planning under RO should pass: %v", err)
	}
	if ResolvedMutationContract() != MutationContractReadOnly {
		t.Fatalf("contract=%s", ResolvedMutationContract())
	}
}

func TestEnforceMutationPolicy_EnvBlocksExecute(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")

	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	prune := &cobra.Command{Use: "prune", Run: func(cmd *cobra.Command, args []string) {}}
	setCommandCapability(prune, CapabilityPlanning)
	var execute bool
	prune.Flags().BoolVarP(&execute, "execute", "e", false, "")
	root.AddCommand(prune)
	if err := prune.Flags().Set("execute", "true"); err != nil {
		t.Fatal(err)
	}
	if err := enforceMutationPolicy(prune); err == nil {
		t.Fatal("env READ_ONLY must block --execute")
	}
}

func TestEnforceMutationPolicy_UnknownCapabilityFailClosed(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "true")

	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	mystery := &cobra.Command{Use: "mystery", Run: func(cmd *cobra.Command, args []string) {}}
	root.AddCommand(mystery)
	if err := enforceMutationPolicy(mystery); err == nil || !strings.Contains(err.Error(), "no declared") {
		t.Fatalf("want fail-closed unknown capability, got %v", err)
	}
}

func TestEnforceMutationPolicy_TypoCapabilityFailClosed(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	// Bypass setCommandCapability validation to simulate a typo annotation.
	bad := &cobra.Command{Use: "weird", Run: func(cmd *cobra.Command, args []string) {}}
	bad.Annotations = map[string]string{annotationCapability: "mutatng"}
	root.AddCommand(bad)
	if err := enforceMutationPolicy(bad); err == nil || !strings.Contains(err.Error(), "unknown mutation capability") {
		t.Fatalf("typo capability must fail closed, got %v", err)
	}
}

func TestEnforceMutationPolicy_UnannotatedPruneFailClosed(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	prune := &cobra.Command{Use: "prune", Run: func(cmd *cobra.Command, args []string) {}}
	prune.Flags().Bool("execute", false, "")
	root.AddCommand(prune)
	if err := enforceMutationPolicy(prune); err == nil || !strings.Contains(err.Error(), "no declared") {
		t.Fatalf("unannotated prune under RO must fail closed, got %v", err)
	}
}

func TestEnforceMutationPolicy_CoverageAttestBlockedUnderRO(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	inv := &cobra.Command{Use: "inventory", Run: func(cmd *cobra.Command, args []string) {}}
	setCommandCapability(inv, CapabilityObservational)
	inv.Flags().String("coverage-attest", "", "")
	root.AddCommand(inv)
	if err := inv.Flags().Set("coverage-attest", "/tmp/attest.json"); err != nil {
		t.Fatal(err)
	}
	if err := enforceMutationPolicy(inv); err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("coverage-attest under RO must be mutating reject, got %v", err)
	}
}

func TestEnforceMutationPolicy_ObserveLeavesRejectedUnderRO(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	// Build the real observe tree so leaf annotations stay wired.
	id := testIdentity()
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	obs := newObserveCmd(id)
	setCommandCapability(obs, CapabilityObservational)
	root.AddCommand(obs)
	for _, name := range []string{"register", "once", "status"} {
		leaf, _, err := root.Find([]string{"observe", name})
		if err != nil {
			t.Fatalf("find observe %s: %v", name, err)
		}
		if err := enforceMutationPolicy(leaf); err == nil || !strings.Contains(err.Error(), "forbids mutating") {
			t.Fatalf("observe %s under RO must be rejected: %v", name, err)
		}
		if got := resolveInvocationCapability(leaf); got != CapabilityMutating {
			t.Fatalf("observe %s capability=%q", name, got)
		}
	}
}

func TestEnforceMutationPolicy_ExemptCompletionUnderEnvRO(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")

	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	complete := &cobra.Command{Use: "__complete", Hidden: true, Run: func(cmd *cobra.Command, args []string) {}}
	root.AddCommand(complete)
	if err := enforceMutationPolicy(complete); err != nil {
		t.Fatalf("completion under RO env must be exempt: %v", err)
	}
}

func TestEnforceMutationPolicy_BareRootUnderRO(t *testing.T) {
	resetPolicyForTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	root := &cobra.Command{Use: "spanwit", RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	}}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	if err := enforceMutationPolicy(root); err != nil {
		t.Fatalf("bare root under RO must be exempt: %v", err)
	}
}

// --- Cobra integration: PreRun rejects before RunE (AC3 + AC5) ---

func testIdentity() *appidentity.Identity {
	return &appidentity.Identity{
		BinaryName:  "spanwit",
		Vendor:      "fulmenhq",
		EnvPrefix:   "SPANWIT_",
		ConfigName:  "spanwit",
		Description: "Test tool",
	}
}

func initRootForMutationTest(t *testing.T) {
	t.Helper()
	resetPolicyForTest(t)
	logger, err := logging.NewCLI("spanwit")
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), testIdentity(), logger)
	t.Cleanup(func() {
		resetMutationPolicyState()
		rootCmd = nil
	})
}

func replacePruneRunE(t *testing.T, sentinel *bool) {
	t.Helper()
	var prune *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "prune" {
			prune = c
			break
		}
	}
	if prune == nil {
		t.Fatal("prune command missing after Initialize")
	}
	prune.RunE = func(cmd *cobra.Command, args []string) error {
		*sentinel = true
		return nil
	}
	// Silence usage noise on expected policy errors.
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
}

func TestIntegration_ReadOnlyBlocksPruneExecuteLongFlag(t *testing.T) {
	initRootForMutationTest(t)
	var runEntered bool
	replacePruneRunE(t, &runEntered)

	rootCmd.SetArgs([]string{"--read-only", "prune", "--execute"})
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want RO+execute rejection, got err=%v stderr=%s", err, stderr.String())
	}
	if runEntered {
		t.Fatal("prune RunE must not run when RO assertion blocks execute (AC3)")
	}
}

func TestIntegration_ReadOnlyBlocksPruneExecuteShortFlag(t *testing.T) {
	initRootForMutationTest(t)
	var runEntered bool
	replacePruneRunE(t, &runEntered)

	// Short -e must elevate to mutating and be rejected under inherited --read-only.
	rootCmd.SetArgs([]string{"--read-only", "prune", "-e"})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want RO+-e rejection, got %v", err)
	}
	if runEntered {
		t.Fatal("prune RunE must not run for -e under --read-only")
	}
}

func TestIntegration_EnvReadOnlyBlocksPruneExecute(t *testing.T) {
	initRootForMutationTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "yes")
	var runEntered bool
	replacePruneRunE(t, &runEntered)

	rootCmd.SetArgs([]string{"prune", "--execute"})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want env RO rejection, got %v", err)
	}
	if runEntered {
		t.Fatal("RunE must not run under SPANWIT_READ_ONLY")
	}
}

func TestIntegration_FlagEnvConflictFailClosed(t *testing.T) {
	initRootForMutationTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "false")
	var runEntered bool
	replacePruneRunE(t, &runEntered)

	rootCmd.SetArgs([]string{"--read-only", "prune"})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("want conflict, got %v", err)
	}
	if runEntered {
		t.Fatal("RunE must not run on assertion conflict")
	}
}

func TestIntegration_ReadOnlyAllowsPruneDryRun(t *testing.T) {
	initRootForMutationTest(t)
	var runEntered bool
	replacePruneRunE(t, &runEntered)

	rootCmd.SetArgs([]string{"--read-only", "prune", "--allowlist", "/tmp/no-such-path"})
	// Dry-run planning under RO is allowed; RunE may run and fail on path — that is OK.
	// We only require that enforce does not reject before RunE.
	_ = rootCmd.Execute()
	if !runEntered {
		// If allowlist fails in PreRun somehow, still check contract was set when enforce ran.
		// Prefer: RunE entered proves enforce allowed planning.
		t.Fatal("RO dry-run prune should reach RunE (planning capability)")
	}
	if ResolvedMutationContract() != MutationContractReadOnly {
		t.Fatalf("want read_only contract after RO dry-run, got %s", ResolvedMutationContract())
	}
}

func TestIntegration_ReadOnlyBlocksObserveBeforeStateWrite(t *testing.T) {
	initRootForMutationTest(t)
	stateDir := filepath.Join(t.TempDir(), "observe-state")
	rootCmd.SetArgs([]string{"--read-only", "observe", "once", "--state-dir", stateDir})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want read-only observe rejection, got %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("read-only rejection must occur before state creation: %v", err)
	}
}

func TestIntegration_ReadOnlyBlocksBareObserveBeforeStateWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  bool
	}{
		{name: "flag"},
		{name: "environment", env: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initRootForMutationTest(t)
			stateDir := filepath.Join(t.TempDir(), "observe-state")
			args := []string{"observe", "--state-dir", stateDir}
			if tc.env {
				t.Setenv("SPANWIT_READ_ONLY", "true")
			} else {
				args = append([]string{"--read-only"}, args...)
			}
			rootCmd.SetArgs(args)
			err := rootCmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
				t.Fatalf("want bare observe rejection, got %v", err)
			}
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("rejection must occur before state creation: %v", err)
			}
		})
	}
}

func TestIntegration_EnvAliasesThroughEnforce(t *testing.T) {
	for _, val := range []string{"on", "YES", "1", "true"} {
		t.Run("env_"+val, func(t *testing.T) {
			initRootForMutationTest(t)
			t.Setenv("SPANWIT_READ_ONLY", val)
			var runEntered bool
			replacePruneRunE(t, &runEntered)
			rootCmd.SetArgs([]string{"prune", "-e"})
			err := rootCmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
				t.Fatalf("env %q should block -e: %v", val, err)
			}
			if runEntered {
				t.Fatal("RunE must not enter")
			}
		})
	}
}

func replaceInventoryRunE(t *testing.T, sentinel *bool) {
	t.Helper()
	var inv *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "inventory" {
			inv = c
			break
		}
	}
	if inv == nil {
		t.Fatal("inventory missing")
	}
	inv.RunE = func(cmd *cobra.Command, args []string) error {
		*sentinel = true
		return nil
	}
	// inventory uses Run not RunE in production — clear Run so RunE is used.
	inv.Run = nil
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
}

func TestIntegration_ReadOnlyBlocksInventoryCoverageAttest(t *testing.T) {
	initRootForMutationTest(t)
	var runEntered bool
	replaceInventoryRunE(t, &runEntered)

	attest := filepath.Join(t.TempDir(), "attest.json")
	rootCmd.SetArgs([]string{
		"--read-only", "inventory", t.TempDir(),
		"--summary-only", "--quiet",
		"--coverage-attest", attest,
	})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want coverage-attest rejection under RO, got %v", err)
	}
	if runEntered {
		t.Fatal("inventory RunE must not run under RO+coverage-attest")
	}
	if _, err := os.Stat(attest); err == nil {
		t.Fatal("attestation file must not be created")
	}
}

func TestIntegration_ReadOnlyBlocksDirectoryAuditCoverageAttest(t *testing.T) {
	initRootForMutationTest(t)
	var runEntered bool
	replaceInventoryRunE(t, &runEntered)

	attest := filepath.Join(t.TempDir(), "attest.json")
	rootCmd.SetArgs([]string{
		"--read-only", "inventory", t.TempDir(),
		"--directories", "--format", "jsonl", "--quiet",
		"--coverage-attest", attest,
	})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forbids mutating") {
		t.Fatalf("want directory-audit coverage-attest rejection under RO, got %v", err)
	}
	if runEntered {
		t.Fatal("inventory RunE must not run under RO+coverage-attest")
	}
	if _, err := os.Stat(attest); err == nil {
		t.Fatal("attestation file must not be created")
	}
}

func TestIntegration_CompletionBashUnderEnvRO(t *testing.T) {
	initRootForMutationTest(t)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	// Ensure completion command tree exists (cobra may auto-register).
	rootCmd.InitDefaultCompletionCmd()
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.SetArgs([]string{"completion", "bash"})
	// Should not fail closed as unknown capability; may write completion script.
	err := rootCmd.Execute()
	if err != nil && strings.Contains(err.Error(), "mutation capability") {
		t.Fatalf("completion bash under RO must not fail on capability: %v", err)
	}
}

func TestIntegration_CompletionZshUnderReadOnlyFlag(t *testing.T) {
	initRootForMutationTest(t)
	rootCmd.InitDefaultCompletionCmd()
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	rootCmd.SetArgs([]string{"--read-only", "completion", "zsh"})
	err := rootCmd.Execute()
	if err != nil && strings.Contains(err.Error(), "mutation capability") {
		t.Fatalf("completion zsh under --read-only must not fail on capability: %v", err)
	}
}

func TestRootCommandsHaveNoLocalPreRunHooks(t *testing.T) {
	// Cobra uses the nearest PersistentPreRunE only unless EnableTraverseRunHooks
	// is set. A leaf-local pre-run would silently disable the root mutation policy.
	initRootForMutationTest(t)
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		t.Helper()
		if c != rootCmd {
			if c.PreRun != nil || c.PreRunE != nil || c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
				t.Fatalf("command %q defines a local pre-run hook that would shadow root mutation policy", c.CommandPath())
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
}

func TestSetCommandCapabilityRejectsTypo(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on invalid capability")
		}
	}()
	setCommandCapability(&cobra.Command{Use: "x"}, "mutatng")
}
