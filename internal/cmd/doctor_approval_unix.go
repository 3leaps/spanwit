//go:build darwin || linux

package cmd

import (
	"os"
	"runtime"

	"github.com/3leaps/spanwit/internal/approval"
)

// homebrewSSHKeygen is where Homebrew's openssh installs ssh-keygen on
// Apple Silicon.
const homebrewSSHKeygen = "/opt/homebrew/bin/ssh-keygen"

// approvalSigningReport describes readiness for approval signing and
// verification. It only checks files and their ownership; it never
// runs ssh-keygen or touches a key, so security-key support itself is
// confirmed by the first signature.
func approvalSigningReport(goos string, exists func(string) bool, trusted, signer func(string) error) []string {
	var lines []string
	if err := trusted(approval.SystemSSHKeygen); err != nil {
		lines = append(lines, "verify: "+approval.SystemSSHKeygen+" unavailable or untrusted ("+err.Error()+")")
	} else {
		lines = append(lines, "verify: "+approval.SystemSSHKeygen)
	}
	if err := trusted(approval.DefaultAllowedSigners); err != nil {
		lines = append(lines, "allowed signers: "+approval.DefaultAllowedSigners+" not usable ("+err.Error()+"); only needed where approvals are executed")
	} else {
		lines = append(lines, "allowed signers: "+approval.DefaultAllowedSigners)
	}
	switch {
	case goos == "darwin" && exists(homebrewSSHKeygen):
		if err := signer(homebrewSSHKeygen); err != nil {
			lines = append(lines, "sign: "+homebrewSSHKeygen+" would be refused ("+err.Error()+")")
		} else {
			lines = append(lines, "sign: approval sign --ssh-keygen "+homebrewSSHKeygen+" (the macOS ssh-keygen cannot use security keys)")
		}
	case goos == "darwin":
		lines = append(lines, "sign: the macOS ssh-keygen cannot use security keys; brew install openssh, then approval sign --ssh-keygen "+homebrewSSHKeygen)
	default:
		lines = append(lines, "sign: "+approval.SystemSSHKeygen+" (security-key support is confirmed by the first signature)")
	}
	return lines
}

func approvalSigningLines() []string {
	return approvalSigningReport(runtime.GOOS,
		func(p string) bool { _, err := os.Stat(p); return err == nil },
		func(p string) error { _, err := approval.TrustedFile(p, 64<<20); return err },
		func(p string) error { _, err := approval.SignerPath(p); return err })
}
