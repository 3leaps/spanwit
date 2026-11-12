package cmd

import (
	"encoding/json"
	"os"

	"github.com/3leaps/spanwit/internal/profiles"
	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	parent := &cobra.Command{Use: "profile", Short: "Validate and preview experimental persona profiles"}
	validate := &cobra.Command{Use: "validate FILE", Short: "Validate a profile pack without activating it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		pack, err := loadProfilePack(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			State    string   `json:"state"`
			Revision string   `json:"revision"`
			Profiles []string `json:"profiles"`
		}{"valid", pack.Revision(), pack.Names()})
	}}
	var name string
	preview := &cobra.Command{Use: "preview FILE", Short: "Preview a persona with the existing planner; never delete", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		pack, err := loadProfilePack(args[0])
		if err != nil {
			return err
		}
		result, err := pack.Preview(cmd.Context(), name)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	preview.Flags().StringVar(&name, "name", "", "persona name within the pack")
	_ = preview.MarkFlagRequired("name")
	setCommandCapability(parent, CapabilityObservational)
	setCommandCapability(validate, CapabilityObservational)
	setCommandCapability(preview, CapabilityObservational)
	diff := &cobra.Command{Use: "diff BEFORE AFTER", Short: "Review recipe and persona edits without activating them", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		before, err := loadProfilePack(args[0])
		if err != nil {
			return err
		}
		after, err := loadProfilePack(args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(before.Diff(after))
	}}
	setCommandCapability(diff, CapabilityObservational)
	parent.AddCommand(validate, preview, diff)
	return parent
}
func loadProfilePack(path string) (*profiles.Pack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return profiles.Load(f)
}
