package approval

import (
	"errors"
	"fmt"
)

// Refusal codes (N8). Every refusal an approved execution can return carries
// exactly one of these, so callers and tests can match the contract name.
const (
	CodeStalePlanOrDrift           = "stale_plan_or_drift"
	CodeUnauthorizedPrincipal      = "unauthorized_principal"
	CodeGrantExpiredOrRevoked      = "grant_expired_or_revoked"
	CodeIdempotencyPayloadMismatch = "idempotency_payload_mismatch"
	CodePersistenceUncertain       = "persistence_uncertain"
	CodeGrantAlreadyConsumed       = "grant_already_consumed"
	CodeAllowedSignersUntrusted    = "allowed_signers_untrusted"
	CodeLimitsExceeded             = "limits_exceeded"
	CodePlatformUnavailable        = "approved_execution_unavailable_on_platform"
)

// Refusal is a contract refusal with a human-readable detail.
type Refusal struct {
	Code   string
	Detail string
	Err    error
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Code
	}
	return r.Code + ": " + r.Detail
}

func (r *Refusal) Unwrap() error { return r.Err }

func refuse(code string, err error, format string, a ...any) error {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, a...), Err: err}
}

// RefusalCode returns the contract code of err, or "" when err is not a
// contract refusal.
func RefusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}
