//go:build darwin || linux

package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/3leaps/spanwit/internal/approval"
	"github.com/3leaps/spanwit/internal/daemon"
)

const approvalPlanLimit = 64 << 20

func newApprovalCmd(identity *appidentity.Identity) *cobra.Command {
	home, _ := os.UserHomeDir()
	binary := "spanwit"
	if identity != nil && identity.BinaryName != "" {
		binary = identity.BinaryName
	}
	stateDir := filepath.Join(home, ".local", "state", binary, "daemon")
	allowedSigners := approval.DefaultAllowedSigners

	parent := &cobra.Command{
		Use:   "approval",
		Short: "One-shot approvals for an exact prune plan (request, show, sign, issue, revoke, status)",
		Long: `One-shot approval of an exact prune plan.

  1. spanwit approval request --plan p.json --out payload.json
     (anyone; journals a request with no authority; the daemon must be stopped)
  2. the maintainer, in their own session:
     spanwit approval sign --plan p.json --payload payload.json --key <security key>
     (shows the candidates, then signs those exact bytes; needs a touch)
  3. spanwit approval issue --payload payload.json --sig payload.json.sig
  4. spanwit prune --from-exact-plan p.json --execute --approval <id>

Only a security-key signature made with user presence, by a key listed in a
trusted allowed-signers file (` + approval.DefaultAllowedSigners + ` by default, not
writable by this user), authorizes an execution. An approval is used once,
expires (at most 24h), and binds the exact plan bytes, --min-age, the
signature catalog and, when requested, the active profile activation.`,
	}
	parent.PersistentFlags().StringVar(&stateDir, "state-dir", stateDir, "coordinator state directory holding the journal")

	var planPath, payloadPath, outPath, sigPath, keyPath, minAge string
	var expires time.Duration
	var bindProfile bool

	request := &cobra.Command{Use: "request", Args: cobra.NoArgs, Short: "Journal an approval request for an exact plan and write the payload to sign",
		RunE: func(cmd *cobra.Command, _ []string) error {
			plan, err := readBounded(planPath, approvalPlanLimit)
			if err != nil {
				return err
			}
			var ma *string
			if cmd.Flags().Changed("min-age") {
				ma = &minAge
			}
			c, err := openApprovalCoordinator(stateDir)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			p, raw, err := approval.Request(cmd.Context(), c, approval.RequestInput{Plan: plan, MinAge: ma, Expires: expires, BindProfile: bindProfile, Now: time.Now()})
			if err != nil {
				return err
			}
			if err := writeNewFile(outPath, raw); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "Approval %s requested (no authority until issued).\n", p.ApprovalID)
			_, _ = fmt.Fprintf(w, "Payload written to %s; expires %s.\n\n", outPath, p.ExpiresAt.Format(time.RFC3339))
			_, _ = fmt.Fprintf(w, "Required next step, by the maintainer in their own session:\n  %s approval sign --plan %s --payload %s --key <security key>\n", binaryName(identity), planPath, outPath)
			return nil
		}}
	request.Flags().StringVar(&planPath, "plan", "", "exact plan JSON to approve (required)")
	request.Flags().StringVar(&outPath, "out", "", "where to write the payload to sign (required; must not exist)")
	request.Flags().DurationVar(&expires, "expires", time.Hour, "approval lifetime (at most 24h)")
	request.Flags().StringVar(&minAge, "min-age", "", "bind this --min-age; execution must pass the same value")
	request.Flags().BoolVar(&bindProfile, "bind-active-profile", false, "bind the active profile activation (profile-derived plans)")
	markRequired(request, "plan", "out")
	setCommandCapability(request, CapabilityMutating)

	show := &cobra.Command{Use: "show", Args: cobra.NoArgs, Short: "Check that a payload binds a plan and print what it would authorize (read-only)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			plan, payload, err := readPlanAndPayload(planPath, payloadPath)
			if err != nil {
				return err
			}
			p, id, err := approval.Show(plan, payload)
			if err != nil {
				return err
			}
			printApprovalSummary(cmd.OutOrStdout(), p, id, plan)
			return nil
		}}
	show.Flags().StringVar(&planPath, "plan", "", "exact plan JSON (required)")
	show.Flags().StringVar(&payloadPath, "payload", "", "approval payload (required)")
	markRequired(show, "plan", "payload")
	setCommandCapability(show, CapabilityObservational)

	sign := &cobra.Command{Use: "sign", Args: cobra.NoArgs, Short: "Maintainer only: show the plan, then sign those exact payload bytes with a security key",
		RunE: func(cmd *cobra.Command, _ []string) error {
			plan, payload, err := readPlanAndPayload(planPath, payloadPath)
			if err != nil {
				return err
			}
			if !stdinIsTerminal() {
				return fmt.Errorf("approval sign needs an interactive terminal for confirmation")
			}
			sig, err := approval.Sign(cmd.Context(), plan, payload, keyPath, func(p approval.Payload, id approval.PlanIdentity) bool {
				printApprovalSummary(cmd.OutOrStdout(), p, id, plan)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nSign approval %s? Type the first 12 characters of the plan digest to confirm: ", p.ApprovalID)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				ok := strings.TrimSpace(line) == strings.TrimPrefix(p.PlanDigest, "sha256:")[:12]
				if ok {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Touch your security key now.")
				}
				return ok
			})
			if err != nil {
				return err
			}
			out := outPath
			if out == "" {
				out = payloadPath + ".sig"
			}
			if err := writeNewFile(out, sig); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Signature written to %s.\n", out)
			return nil
		}}
	sign.Flags().StringVar(&planPath, "plan", "", "exact plan JSON (required)")
	sign.Flags().StringVar(&payloadPath, "payload", "", "approval payload (required)")
	sign.Flags().StringVar(&keyPath, "key", "", "security-key (sk-) private key handle (required)")
	sign.Flags().StringVar(&outPath, "out", "", "signature output (default <payload>.sig; must not exist)")
	markRequired(sign, "plan", "payload", "key")
	setCommandCapability(sign, CapabilityPlanning)

	issue := &cobra.Command{Use: "issue", Args: cobra.NoArgs, Short: "Verify the maintainer's signature and record the approval",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload, err := readBounded(payloadPath, 64<<10)
			if err != nil {
				return err
			}
			sig, err := readBounded(sigPath, 16<<10)
			if err != nil {
				return err
			}
			c, err := openApprovalCoordinator(stateDir)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			head, v, err := approval.Issue(cmd.Context(), c, payload, sig, allowedSigners, time.Now())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Approval %s issued by %s (%s, %s).\n", head.Record.ApprovalID, v.Principal, v.KeyType, v.Assurance)
			return nil
		}}
	issue.Flags().StringVar(&payloadPath, "payload", "", "approval payload (required)")
	issue.Flags().StringVar(&sigPath, "sig", "", "maintainer signature (required)")
	issue.Flags().StringVar(&allowedSigners, "allowed-signers", allowedSigners, "trusted allowed-signers file (not writable by this user)")
	markRequired(issue, "payload", "sig")
	setCommandCapability(issue, CapabilityMutating)

	revoke := &cobra.Command{Use: "revoke <approval-id>", Args: cobra.ExactArgs(1), Short: "Revoke a requested or issued approval",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openApprovalCoordinator(stateDir)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			if _, err := approval.Revoke(cmd.Context(), c, args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Approval %s revoked.\n", args[0])
			return nil
		}}
	setCommandCapability(revoke, CapabilityMutating)

	status := &cobra.Command{Use: "status <approval-id>", Args: cobra.ExactArgs(1), Short: "Show an approval's journaled state",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openApprovalCoordinator(stateDir)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			head, err := c.LoadApproval(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := map[string]any{"approval_id": head.Record.ApprovalID, "state": head.Record.State, "revision": head.Pin.Revision, "updated_at": head.Record.UpdatedAt}
			if head.Record.Decision != nil {
				out["decision"] = head.Record.Decision // display only; execution re-verifies
			}
			if head.Record.Consumption != nil {
				out["operation_id"] = head.Record.Consumption.OperationID
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}}
	setCommandCapability(status, CapabilityObservational)

	parent.AddCommand(request, show, sign, issue, revoke, status)
	return parent
}

func binaryName(identity *appidentity.Identity) string {
	if identity != nil && identity.BinaryName != "" {
		return identity.BinaryName
	}
	return "spanwit"
}

func openApprovalCoordinator(stateDir string) (*daemon.Coordinator, error) {
	c, err := daemon.Open(stateDir, nil)
	if err != nil {
		if errorsIsBusy(err) {
			return nil, fmt.Errorf("writer_busy: the spanwit daemon holds %s; stop it (`spanwit daemon service stop`), then retry", stateDir)
		}
		return nil, err
	}
	return c, nil
}

func errorsIsBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), daemon.ErrBusy.Error())
}

func markRequired(c *cobra.Command, names ...string) {
	for _, n := range names {
		_ = c.MarkFlagRequired(n)
	}
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- an operator-named input; its content is digest-bound.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, nil
}

func readPlanAndPayload(planPath, payloadPath string) ([]byte, []byte, error) {
	plan, err := readBounded(planPath, approvalPlanLimit)
	if err != nil {
		return nil, nil, err
	}
	payload, err := readBounded(payloadPath, 64<<10)
	if err != nil {
		return nil, nil, err
	}
	return plan, payload, nil
}

// writeNewFile writes data to a path that must not exist yet (0600).
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- an operator-named output, created exclusively.
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func printApprovalSummary(w io.Writer, p approval.Payload, id approval.PlanIdentity, plan []byte) {
	_, _ = fmt.Fprintf(w, "Approval:        %s (one shot)\n", p.ApprovalID)
	_, _ = fmt.Fprintf(w, "Analysis root:   %s\n", p.AnalysisRoot)
	_, _ = fmt.Fprintf(w, "Plan digest:     %s\n", p.PlanDigest)
	_, _ = fmt.Fprintf(w, "Limits:          at most %d candidates, %s\n", p.Limits.MaxCandidates, humanSize(p.Limits.MaxReclaimBytes))
	minAge := "(not given)"
	if p.EffectiveOptions.MinAge != nil {
		minAge = *p.EffectiveOptions.MinAge
	}
	_, _ = fmt.Fprintf(w, "--min-age:       %s\n", minAge)
	policy := "none (hand-built plan; binds the signature catalog only)"
	if p.PolicyRef != nil {
		policy = fmt.Sprintf("profile pack %s, activation revision %d", p.PolicyRef.PackRevision, p.PolicyRef.ProfileHead.Revision)
	}
	_, _ = fmt.Fprintf(w, "Policy:          %s\n", policy)
	_, _ = fmt.Fprintf(w, "Window:          %s to %s\n", p.IssuedAt.Format(time.RFC3339), p.ExpiresAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "Candidates (%d):\n", id.Candidates)
	for _, c := range approvalCandidates(plan) {
		_, _ = fmt.Fprintf(w, "  %s\n", c)
	}
}

func approvalCandidates(plan []byte) []string {
	var doc struct {
		Candidates []struct {
			Path      string `json:"path"`
			Signature string `json:"signature"`
			Pattern   string `json:"pattern"`
			SizeBytes int64  `json:"size_bytes"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(plan, &doc); err != nil {
		return []string{"(unreadable)"}
	}
	var out []string
	for _, c := range doc.Candidates {
		id := c.Signature
		if id == "" {
			id = c.Pattern
		}
		out = append(out, fmt.Sprintf("%-10s %s  [%s]", humanSize(c.SizeBytes), c.Path, id))
	}
	return out
}

// stdinIsTerminal reports whether standard input is a terminal (a window-size
// query succeeds only on a tty; /dev/null and pipes fail it).
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetWinsize(unix.Stdin, unix.TIOCGWINSZ)
	return err == nil
}
