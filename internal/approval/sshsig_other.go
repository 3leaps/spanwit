//go:build !(darwin || linux)

package approval

import (
	"context"
	"errors"
)

// SignatureNamespace is the SSHSIG namespace every approval signature uses.
const SignatureNamespace = "spanwit-approval"

// Refusal classes from the approval contract.
var (
	ErrUnauthorizedPrincipal   = errors.New("unauthorized_principal")
	ErrAllowedSignersUntrusted = errors.New("allowed_signers_untrusted")
	ErrVerifierUnavailable     = errors.New("verifier_unavailable")
)

// Verified is the result of a successful A1 verification.
type Verified struct {
	Principal string
	KeyType   string
	Assurance string
}

// Verify is unavailable on this platform: approvals cannot be verified, so
// no approved execution can proceed.
func Verify(context.Context, []byte, []byte, string) (Verified, error) {
	return Verified{}, ErrVerifierUnavailable
}
