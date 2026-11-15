package canonjson

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCanonicalizeVectors(t *testing.T) {
	cases := []struct{ in, want string }{
		{`null`, `null`},
		{` true `, `true`},
		{`{"b":1,"a":[3,2,1],"c":{"z":null,"y":false}}`, `{"a":[3,2,1],"b":1,"c":{"y":false,"z":null}}`},
		{"{ \"a\" : \"x\" }", `{"a":"x"}`},
		{`"\u0008\t\n\u000c\r\u0001\u001f"`, `"\b\t\n\f\r\u0001\u001f"`},
		{`"\"\\\/"`, `"\"\\/"`},
		{`"é😀"`, "\"é\U0001F600\""},
		{"\"é\"", "\"é\""},
		{`"\u007f"`, "\"\x7f\""},
		{`9007199254740991`, `9007199254740991`},
		{`-9007199254740991`, `-9007199254740991`},
		{`-0`, `0`},
		{`[]`, `[]`},
		{`{}`, `{}`},
		// Code point order, not UTF-16 order: U+FF5E sorts before U+1F600.
		{"{\"\U0001F600\":1,\"～\":2,\"a\":3}", "{\"a\":3,\"～\":2,\"\U0001F600\":1}"},
	}
	for _, c := range cases {
		got, err := Canonicalize([]byte(c.in))
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalizeRejects(t *testing.T) {
	cases := map[string]string{
		"duplicate member":     `{"a":1,"a":1}`,
		"duplicate via escape": `{"a":1,"\u0061":2}`,
		"fraction":             `1.0`,
		"exponent":             `1e3`,
		"capital exponent":     `1E3`,
		"too large":            `9007199254740992`,
		"too small":            `-9007199254740992`,
		"long token":           `100000000000000000000`,
		"leading zero":         `01`,
		"lone high surrogate":  `"\ud800"`,
		"lone low surrogate":   `"\udc00"`,
		"high then non-low":    `"\ud800A"`,
		"invalid utf8":         "\"\xff\"",
		"overlong utf8":        "\"\xc0\xaf\"",
		"encoded surrogate":    "\"\xed\xa0\x80\"",
		"raw control":          "\"a\x01\"",
		"bad escape":           `"\x"`,
		"trailing data":        `{} {}`,
		"trailing comma":       `[1,]`,
		"nan":                  `NaN`,
		"plus sign":            `+1`,
		"empty":                ``,
		"unterminated":         `{"a":1`,
		"single quotes":        `{'a':1}`,
	}
	for name, in := range cases {
		if _, err := Canonicalize([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %q accepted (err=%v)", name, in, err)
		}
	}
}

func TestDepthBound(t *testing.T) {
	deep := strings.Repeat("[", maxDepth+2) + strings.Repeat("]", maxDepth+2)
	if _, err := Parse([]byte(deep)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-deep input accepted: %v", err)
	}
	ok := strings.Repeat("[", maxDepth) + strings.Repeat("]", maxDepth)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("depth %d rejected: %v", maxDepth, err)
	}
}

func TestEncodeRejectsUnsafeGoValues(t *testing.T) {
	for _, v := range []Value{1.5, float64(1), int64(MaxSafeInteger + 1), uint(1), map[string]any{"a": 1}, Object{"a": "\xff"}} {
		if _, err := Encode(v); !errors.Is(err, ErrInvalid) {
			t.Errorf("%T %v accepted", v, v)
		}
	}
}

func TestEncodeParseRoundTrip(t *testing.T) {
	in := `{"z":[1,"two",{"k":null}],"a":-5,"m":"line\nbreak"}`
	first, err := Canonicalize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Canonicalize(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("not idempotent: %s vs %s", first, second)
	}
}

// TestDigestVector pins the domain-separation layout: SHA-256 over
// domain || 0x00 || canonical bytes. Expected value computed independently:
// printf 'application-control/v0/request-fingerprint\0{"a":1}' | shasum -a 256
func TestDigestVector(t *testing.T) {
	got, err := Digest("application-control/v0/request-fingerprint", Object{"a": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if got != digestVectorWant {
		t.Fatalf("digest %s", got)
	}
}

// TestApplicationControlFixture recomputes the request fingerprint of the
// application-control/v0 conforming fixture (copied from crucible) to pin
// interoperability with the published contract.
func TestApplicationControlFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/app-control-v0-layout-set-request.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	req := v.(Object)
	proj := Object{}
	for k, x := range req {
		if k != "request_id" && k != "request_fingerprint" {
			proj[k] = x
		}
	}
	got, err := Digest("application-control/v0/request-fingerprint", proj)
	if err != nil {
		t.Fatal(err)
	}
	if got != req["request_fingerprint"] {
		t.Fatalf("fingerprint %s, fixture claims %v", got, req["request_fingerprint"])
	}
}
