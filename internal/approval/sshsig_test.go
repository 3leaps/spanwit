//go:build darwin || linux

package approval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test-only software security-key signer. The production binary contains no
// signer: approvals are signed by the maintainer's hardware key via ssh-keygen.

func wstr(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	return out
}

type softSK struct {
	priv    ed25519.PrivateKey
	keyType string
	app     string
}

func newSoftSK(t *testing.T) softSK {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return softSK{priv: priv, keyType: "sk-ssh-ed25519@openssh.com", app: "ssh:"}
}

func (k softSK) pubBlob() []byte {
	var b []byte
	b = append(b, wstr([]byte(k.keyType))...)
	b = append(b, wstr(k.priv.Public().(ed25519.PublicKey))...)
	return append(b, wstr([]byte(k.app))...)
}

func (k softSK) authorizedKey() string {
	return k.keyType + " " + base64.StdEncoding.EncodeToString(k.pubBlob())
}

// sign produces an armored SSHSIG over msg with the given namespace and
// security-key flags (0x01 = user present).
func (k softSK) sign(msg []byte, namespace string, flags byte) []byte {
	h := sha512.Sum512(msg)
	var signed []byte
	signed = append(signed, "SSHSIG"...)
	signed = append(signed, wstr([]byte(namespace))...)
	signed = append(signed, wstr(nil)...)
	signed = append(signed, wstr([]byte("sha512"))...)
	signed = append(signed, wstr(h[:])...)
	appHash := sha256.Sum256([]byte(k.app))
	dataHash := sha256.Sum256(signed)
	counter := []byte{0, 0, 0, 7}
	var skMsg []byte
	skMsg = append(skMsg, appHash[:]...)
	skMsg = append(skMsg, flags)
	skMsg = append(skMsg, counter...)
	skMsg = append(skMsg, dataHash[:]...)
	raw := ed25519.Sign(k.priv, skMsg)
	var sigBlob []byte
	sigBlob = append(sigBlob, wstr([]byte(k.keyType))...)
	sigBlob = append(sigBlob, wstr(raw)...)
	sigBlob = append(sigBlob, flags)
	sigBlob = append(sigBlob, counter...)
	var blob []byte
	blob = append(blob, "SSHSIG"...)
	blob = append(blob, 0, 0, 0, 1)
	blob = append(blob, wstr(k.pubBlob())...)
	blob = append(blob, wstr([]byte(namespace))...)
	blob = append(blob, wstr(nil)...)
	blob = append(blob, wstr([]byte("sha512"))...)
	blob = append(blob, wstr(sigBlob)...)
	enc := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(enc) > 70 {
		out.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	out.WriteString(enc + "\n-----END SSH SIGNATURE-----\n")
	return []byte(out.String())
}

// trustAsOther replaces the other-owner predicate (the single approved test
// seam) so files owned by the test user count as another owner's.
func trustAsOther(t *testing.T) {
	prev := otherOwner
	otherOwner = func(uint32) bool { return true }
	t.Cleanup(func() { otherOwner = prev })
}

// trustedDir makes a directory whose ancestors have no group/other write.
// t.TempDir is unsuitable on Linux, where /tmp is world-writable (and so,
// correctly, untrusted).
func trustedDir(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".trust-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeSigners(t *testing.T, dir, content string) string {
	p := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func requireKeygen(t *testing.T) {
	if _, err := os.Stat(sshKeygenPath); err != nil {
		t.Skipf("%s not available", sshKeygenPath)
	}
}

func TestVerifyAcceptsPresenceSignature(t *testing.T) {
	requireKeygen(t)
	trustAsOther(t)
	k := newSoftSK(t)
	signers := writeSigners(t, trustedDir(t), "maintainer@example "+k.authorizedKey()+"\n")
	payload := []byte(`{"approval_id":"a1"}`)
	v, err := Verify(context.Background(), payload, k.sign(payload, SignatureNamespace, 0x01), signers)
	if err != nil {
		t.Fatalf("valid presence signature refused: %v", err)
	}
	if v.Principal != "maintainer@example" || v.Assurance != "A1" || v.KeyType != k.keyType {
		t.Fatalf("verified %+v", v)
	}
}

func TestVerifyRefusals(t *testing.T) {
	requireKeygen(t)
	trustAsOther(t)
	k := newSoftSK(t)
	other := newSoftSK(t)
	dir := trustedDir(t)
	signers := writeSigners(t, dir, "maintainer@example "+k.authorizedKey()+"\n")
	payload := []byte(`{"approval_id":"a1"}`)
	good := k.sign(payload, SignatureNamespace, 0x01)
	cases := map[string]struct {
		payload, sig []byte
	}{
		"user presence clear":   {payload, k.sign(payload, SignatureNamespace, 0x00)},
		"wrong namespace":       {payload, k.sign(payload, "file", 0x01)},
		"payload mutated":       {[]byte(`{"approval_id":"a2"}`), good},
		"key not allowed":       {payload, other.sign(payload, SignatureNamespace, 0x01)},
		"garbage":               {payload, []byte("not a signature")},
		"truncated armor":       {payload, good[:len(good)/2]},
		"bit flipped signature": {payload, flipSigByte(good)},
	}
	for name, c := range cases {
		_, err := Verify(context.Background(), c.payload, c.sig, signers)
		if !errors.Is(err, ErrUnauthorizedPrincipal) {
			t.Errorf("%s: got %v, want unauthorized_principal", name, err)
		}
	}
}

// flipSigByte corrupts a byte inside the signature blob while keeping the
// armor and structure decodable.
func flipSigByte(armored []byte) []byte {
	text := string(armored)
	lines := strings.Split(strings.TrimSpace(text), "\n")
	body := strings.Join(lines[1:len(lines)-1], "")
	blob, _ := base64.StdEncoding.DecodeString(body)
	blob[len(blob)-20] ^= 0xff
	return []byte("-----BEGIN SSH SIGNATURE-----\n" + base64.StdEncoding.EncodeToString(blob) + "\n-----END SSH SIGNATURE-----\n")
}

func TestAllowedSignersUntrusted(t *testing.T) {
	requireKeygen(t)
	k := newSoftSK(t)
	payload := []byte("p")
	sig := k.sign(payload, SignatureNamespace, 0x01)
	line := "maintainer@example " + k.authorizedKey() + "\n"

	t.Run("owned by executing user", func(t *testing.T) {
		// No seam: the real predicate refuses a file this user owns.
		signers := writeSigners(t, trustedDir(t), line)
		if _, err := Verify(context.Background(), payload, sig, signers); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		trustAsOther(t)
		if _, err := Verify(context.Background(), payload, sig, filepath.Join(trustedDir(t), "absent")); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		trustAsOther(t)
		signers := writeSigners(t, trustedDir(t), line)
		if err := os.Chmod(signers, 0o664); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(context.Background(), payload, sig, signers); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("ancestor other writable", func(t *testing.T) {
		trustAsOther(t)
		dir := trustedDir(t)
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		signers := writeSigners(t, sub, line)
		if err := os.Chmod(sub, 0o757); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(context.Background(), payload, sig, signers); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		trustAsOther(t)
		dir := trustedDir(t)
		real := writeSigners(t, dir, line)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(context.Background(), payload, sig, link); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("relative path", func(t *testing.T) {
		trustAsOther(t)
		if _, err := Verify(context.Background(), payload, sig, "allowed_signers"); !errors.Is(err, ErrAllowedSignersUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	for name, content := range map[string]string{
		"ordinary key":      "m ssh-ed25519 " + base64.StdEncoding.EncodeToString(append(wstr([]byte("ssh-ed25519")), wstr(make([]byte, 32))...)) + "\n" + line,
		"no-touch-required": "m no-touch-required " + k.authorizedKey() + "\n",
		"quoted no-touch":   `m namespaces="spanwit-approval",no-touch-required ` + k.authorizedKey() + "\n",
		"cert-authority":    "m cert-authority " + k.authorizedKey() + "\n",
		"blob type mismatch": "m sk-ecdsa-sha2-nistp256@openssh.com " +
			base64.StdEncoding.EncodeToString(k.pubBlob()) + "\n",
		"unterminated quote": `m namespaces="x ` + k.authorizedKey() + "\n",
		"empty":              "# nothing\n",
	} {
		t.Run(name, func(t *testing.T) {
			trustAsOther(t)
			signers := writeSigners(t, trustedDir(t), content)
			if _, err := Verify(context.Background(), payload, sig, signers); !errors.Is(err, ErrAllowedSignersUntrusted) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestParseSignatureFields(t *testing.T) {
	k := newSoftSK(t)
	s, err := ParseSignature(k.sign([]byte("m"), SignatureNamespace, 0x05))
	if err != nil {
		t.Fatal(err)
	}
	if s.Flags != 0x05 || s.Counter != 7 || s.Namespace != SignatureNamespace || !bytes.Equal(s.PublicKey, k.pubBlob()) {
		t.Fatalf("parsed %+v", s)
	}
}
