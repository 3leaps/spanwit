//go:build darwin || linux

package approval

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/3leaps/spanwit/internal/serviceexec"
)

// sshKeygenPath is the only ssh-keygen spanwit runs; PATH is never searched.
var sshKeygenPath = "/usr/bin/ssh-keygen"

// Refusal classes from the approval contract.
var (
	ErrUnauthorizedPrincipal   = errors.New("unauthorized_principal")
	ErrAllowedSignersUntrusted = errors.New("allowed_signers_untrusted")
	ErrVerifierUnavailable     = errors.New("verifier_unavailable")
)

// Security-key algorithms that cannot sign without the device; the only key
// types an approval may be signed with.
var presenceKeyTypes = map[string]bool{
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

const (
	skFlagUserPresence = 0x01
	maxAllowedSigners  = 64 << 10
	maxSignature       = 16 << 10
)

// otherOwner reports whether uid is an owner the executing user cannot act
// as. Tests replace it (the only test seam on the trust predicate).
var otherOwner = func(uid uint32) bool { return int(uid) != os.Getuid() }

// TrustedFile checks that path names a regular file that neither the file
// nor any directory on the way to it can be changed by the executing user:
// every component (including symlink entries and their resolved targets) is
// owned by another user and has no group or other write; the final component
// itself is not a symlink. It returns the file's contents read through a
// no-follow descriptor that is identity-checked against the path.
func TrustedFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: %s is not an absolute clean path", ErrAllowedSignersUntrusted, path)
	}
	if err := trustedChain(path); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllowedSignersUntrusted, err)
	}
	if resolved != path {
		if err := trustedChain(resolved); err != nil {
			return nil, err
		}
	}
	// #nosec G304 -- an absolute, trust-checked configuration path, opened without following links.
	f, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAllowedSignersUntrusted, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrAllowedSignersUntrusted, path)
	}
	checked, err := os.Lstat(resolved)
	if err != nil || !os.SameFile(opened, checked) {
		return nil, fmt.Errorf("%w: %s changed while opening", ErrAllowedSignersUntrusted, path)
	}
	if err := trustedEntry(resolved, opened); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s unreadable or larger than %d bytes", ErrAllowedSignersUntrusted, path, limit)
	}
	return data, nil
}

// trustedChain checks every component of path, from the root down, with
// Lstat. A symlink is acceptable only as an intermediate entry (its target is
// checked separately); the final component must not be one.
func trustedChain(path string) error {
	chain := []string{path}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		chain = append(chain, dir)
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for _, cur := range chain {
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAllowedSignersUntrusted, err)
		}
		if cur == path && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink", ErrAllowedSignersUntrusted, path)
		}
		if err := trustedEntry(cur, info); err != nil {
			return err
		}
	}
	return nil
}

func trustedEntry(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: no ownership for %s", ErrAllowedSignersUntrusted, path)
	}
	if !otherOwner(st.Uid) {
		return fmt.Errorf("%w: %s is owned by the executing user", ErrAllowedSignersUntrusted, path)
	}
	if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s is group or other writable", ErrAllowedSignersUntrusted, path)
	}
	return nil
}

// AllowedSigner is one parsed allowed-signers entry.
type AllowedSigner struct {
	Principals []string
	Options    []string
	KeyType    string
	Key        []byte // decoded public key blob
}

// ParseAllowedSigners parses an OpenSSH allowed-signers file and refuses the
// whole file if any entry uses a key type that can sign without user
// presence, carries no-touch-required or cert-authority, or cannot be parsed
// unambiguously.
func ParseAllowedSigners(data []byte) ([]AllowedSigner, error) {
	var out []AllowedSigner
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields, err := splitAllowedSignersLine(line)
		if err != nil || len(fields) < 3 {
			return nil, fmt.Errorf("%w: line %d unparseable", ErrAllowedSignersUntrusted, n+1)
		}
		e := AllowedSigner{Principals: strings.Split(fields[0], ",")}
		rest := fields[1:]
		if !isKeyTypeToken(rest[0]) {
			e.Options = splitOptions(rest[0])
			rest = rest[1:]
		}
		if len(rest) < 2 {
			return nil, fmt.Errorf("%w: line %d has no key", ErrAllowedSignersUntrusted, n+1)
		}
		e.KeyType = rest[0]
		if !presenceKeyTypes[e.KeyType] {
			return nil, fmt.Errorf("%w: line %d key type %s does not require user presence", ErrAllowedSignersUntrusted, n+1, e.KeyType)
		}
		for _, o := range e.Options {
			name := strings.ToLower(strings.SplitN(o, "=", 2)[0])
			if name == "no-touch-required" || name == "cert-authority" {
				return nil, fmt.Errorf("%w: line %d option %s is not allowed", ErrAllowedSignersUntrusted, n+1, name)
			}
		}
		key, err := base64.StdEncoding.DecodeString(rest[1])
		if err != nil {
			return nil, fmt.Errorf("%w: line %d key is not base64", ErrAllowedSignersUntrusted, n+1)
		}
		r := &wireReader{b: key}
		if kt := r.str(); r.err != nil || string(kt) != e.KeyType {
			return nil, fmt.Errorf("%w: line %d key blob type mismatch", ErrAllowedSignersUntrusted, n+1)
		}
		e.Key = key
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no entries", ErrAllowedSignersUntrusted)
	}
	return out, nil
}

func isKeyTypeToken(s string) bool {
	return strings.HasPrefix(s, "ssh-") || strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "ecdsa-")
}

// splitAllowedSignersLine splits on unquoted whitespace, keeping quoted
// option values intact; an unterminated quote is an error.
func splitAllowedSignersLine(line string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, errors.New("unterminated quote")
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields, nil
}

func splitOptions(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, cur.String())
}

// Signature is a parsed SSHSIG signature.
type Signature struct {
	PublicKey []byte
	KeyType   string
	Namespace string
	HashAlg   string
	Flags     byte
	Counter   uint32
}

// ParseSignature decodes an armored SSHSIG signature and requires the
// spanwit namespace, a security-key algorithm and the user-presence flag.
// ssh-keygen accepts signatures whose user-presence flag is clear, so this
// check is spanwit's own and is applied to the same bytes ssh-keygen verifies.
func ParseSignature(armored []byte) (Signature, error) {
	if len(armored) > maxSignature {
		return Signature{}, fmt.Errorf("%w: signature too large", ErrUnauthorizedPrincipal)
	}
	text := strings.TrimSpace(string(armored))
	const begin, end = "-----BEGIN SSH SIGNATURE-----", "-----END SSH SIGNATURE-----"
	if !strings.HasPrefix(text, begin) || !strings.HasSuffix(text, end) {
		return Signature{}, fmt.Errorf("%w: not an armored SSH signature", ErrUnauthorizedPrincipal)
	}
	body := strings.Join(strings.Fields(text[len(begin):len(text)-len(end)]), "")
	blob, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return Signature{}, fmt.Errorf("%w: signature is not base64", ErrUnauthorizedPrincipal)
	}
	if !bytes.HasPrefix(blob, []byte("SSHSIG")) {
		return Signature{}, fmt.Errorf("%w: missing SSHSIG magic", ErrUnauthorizedPrincipal)
	}
	r := &wireReader{b: blob[6:]}
	version := r.u32()
	var s Signature
	s.PublicKey = r.str()
	s.Namespace = string(r.str())
	_ = r.str() // reserved
	s.HashAlg = string(r.str())
	sigBlob := r.str()
	if r.err != nil || len(r.b) != 0 || version != 1 {
		return Signature{}, fmt.Errorf("%w: malformed SSHSIG", ErrUnauthorizedPrincipal)
	}
	pk := &wireReader{b: s.PublicKey}
	s.KeyType = string(pk.str())
	sr := &wireReader{b: sigBlob}
	sigType := string(sr.str())
	_ = sr.str() // signature bytes; verified by ssh-keygen
	if pk.err != nil || sr.err != nil {
		return Signature{}, fmt.Errorf("%w: malformed signature blob", ErrUnauthorizedPrincipal)
	}
	if s.Namespace != SignatureNamespace {
		return Signature{}, fmt.Errorf("%w: namespace %q", ErrUnauthorizedPrincipal, s.Namespace)
	}
	if !presenceKeyTypes[s.KeyType] || sigType != s.KeyType {
		return Signature{}, fmt.Errorf("%w: key type %s cannot prove user presence", ErrUnauthorizedPrincipal, s.KeyType)
	}
	s.Flags = sr.byte1()
	s.Counter = sr.u32()
	if sr.err != nil || len(sr.b) != 0 {
		return Signature{}, fmt.Errorf("%w: malformed security-key signature", ErrUnauthorizedPrincipal)
	}
	if s.Flags&skFlagUserPresence == 0 {
		return Signature{}, fmt.Errorf("%w: signature made without user presence", ErrUnauthorizedPrincipal)
	}
	return s, nil
}

// Verified is the result of a successful A1 verification. It is derived from
// verification every time and never read back from a stored label.
type Verified struct {
	Principal string
	KeyType   string
	Assurance string // always "A1"
}

// Verify checks sig over payload against the trusted allowed-signers file.
// Order: trust the file and the verifier binary; parse the signature (spanwit
// namespace, presence key type, user-presence flag); find the principal and
// verify with ssh-keygen, handing it the same in-memory payload (stdin) and
// signature (pipe on fd 3) so no path can be swapped between checks; finally
// require the signing key to be the principal's allowed key.
func Verify(ctx context.Context, payload, sig []byte, allowedSignersPath string) (Verified, error) {
	data, err := TrustedFile(allowedSignersPath, maxAllowedSigners)
	if err != nil {
		return Verified{}, err
	}
	signers, err := ParseAllowedSigners(data)
	if err != nil {
		return Verified{}, err
	}
	if _, err := TrustedFile(sshKeygenPath, 64<<20); err != nil {
		return Verified{}, fmt.Errorf("%w: %s: %v", ErrVerifierUnavailable, sshKeygenPath, err)
	}
	parsed, err := ParseSignature(sig)
	if err != nil {
		return Verified{}, err
	}
	run := func(args ...string) serviceexec.Result {
		return serviceexec.ExecRunner{}.Run(ctx, serviceexec.Request{
			Program: sshKeygenPath, Args: args, Dir: "/",
			Timeout: 10 * time.Second, Stdin: payload, ExtraInputs: [][]byte{sig},
		})
	}
	found := run("-Y", "find-principals", "-s", "/dev/fd/3", "-f", allowedSignersPath)
	if found.Outcome != serviceexec.Success {
		return Verified{}, fmt.Errorf("%w: signer not in allowed signers", ErrUnauthorizedPrincipal)
	}
	principals := strings.Fields(string(found.Stdout))
	if len(principals) != 1 {
		return Verified{}, fmt.Errorf("%w: signer must match exactly one principal", ErrUnauthorizedPrincipal)
	}
	principal := principals[0]
	verified := run("-Y", "verify", "-f", allowedSignersPath, "-I", principal, "-n", SignatureNamespace, "-s", "/dev/fd/3")
	if verified.Outcome != serviceexec.Success {
		return Verified{}, fmt.Errorf("%w: signature does not verify", ErrUnauthorizedPrincipal)
	}
	match := false
	for _, s := range signers {
		if bytes.Equal(s.Key, parsed.PublicKey) && contains(s.Principals, principal) {
			match = true
		}
	}
	if !match {
		return Verified{}, fmt.Errorf("%w: signing key is not the principal's allowed key", ErrUnauthorizedPrincipal)
	}
	return Verified{Principal: principal, KeyType: parsed.KeyType, Assurance: "A1"}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type wireReader struct {
	b   []byte
	err error
}

func (r *wireReader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	v := binary.BigEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *wireReader) byte1() byte {
	if r.err != nil || len(r.b) < 1 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *wireReader) str() []byte {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(len(r.b)) {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}
