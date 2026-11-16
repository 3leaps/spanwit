package approval

// SignatureNamespace is the SSHSIG namespace every approval signature uses.
const SignatureNamespace = "spanwit-approval"

// DefaultAllowedSigners is where the maintainer's trusted approval keys live.
// It must not be writable by the user spanwit runs as (see TrustedFile).
const DefaultAllowedSigners = "/etc/spanwit/allowed_signers"
