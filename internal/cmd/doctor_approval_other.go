//go:build !(darwin || linux)

package cmd

func approvalSigningLines() []string {
	return []string{"approvals are unavailable on this platform"}
}
