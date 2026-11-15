// Package canonjson implements Canonical JSON v0 as defined by the
// application-control/v0 semantic contract: only null, booleans, strings,
// integers in [-(2^53-1), 2^53-1], arrays and objects; no fractions or
// exponents; duplicate member names and invalid Unicode rejected before
// canonicalisation; members sorted by Unicode scalar value; arrays in order;
// no insignificant whitespace; fixed string escapes.
//
// Parse reads the raw bytes with its own strict tokenizer rather than
// encoding/json, which silently replaces invalid UTF-8 and lone surrogates,
// keeps the last of duplicate members, and round-trips numbers through
// float64.
package canonjson

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxSafeInteger is the largest magnitude Canonical JSON v0 admits.
const MaxSafeInteger = 9007199254740991

// maxDepth bounds nesting so hostile input cannot exhaust the stack.
const maxDepth = 128

// ErrInvalid is wrapped by every rejection.
var ErrInvalid = errors.New("canonjson: invalid input")

// Value is a parsed Canonical JSON v0 value: nil, bool, int64, string,
// []Value or Object.
type Value any

// Object is a JSON object whose member names are unique.
type Object map[string]Value

// Parse strictly decodes one JSON value from data (surrounding whitespace
// allowed, nothing else).
func Parse(data []byte) (Value, error) {
	p := &parser{data: data}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.data) {
		return nil, p.errf("trailing data")
	}
	return v, nil
}

// Canonicalize parses data and returns its canonical encoding.
func Canonicalize(data []byte) ([]byte, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return Encode(v)
}

// Encode returns the canonical encoding of v. v may be built from the Value
// types above; int and int64 values outside the safe range, floats and any
// other Go type are rejected.
func Encode(v Value) ([]byte, error) {
	return appendValue(nil, v, 0)
}

// Digest returns lowercase hex SHA-256 over domain || 0x00 || canonical(v).
func Digest(domain string, v Value) (string, error) {
	enc, err := Encode(v)
	if err != nil {
		return "", err
	}
	return DigestCanonical(domain, enc), nil
}

// DigestCanonical hashes already-canonical bytes under domain.
func DigestCanonical(domain string, canonical []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

func appendValue(b []byte, v Value, depth int) ([]byte, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: nesting deeper than %d", ErrInvalid, maxDepth)
	}
	switch t := v.(type) {
	case nil:
		return append(b, "null"...), nil
	case bool:
		return strconv.AppendBool(b, t), nil
	case int:
		return appendInt(b, int64(t))
	case int64:
		return appendInt(b, t)
	case string:
		if !utf8.ValidString(t) {
			return nil, fmt.Errorf("%w: string is not valid UTF-8", ErrInvalid)
		}
		return appendString(b, t), nil
	case []Value:
		b = append(b, '[')
		for i, e := range t {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = appendValue(b, e, depth+1); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	case Object:
		keys := make([]string, 0, len(t))
		for k := range t {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("%w: member name is not valid UTF-8", ErrInvalid)
			}
			keys = append(keys, k)
		}
		// Byte order of valid UTF-8 equals Unicode scalar value order.
		sort.Strings(keys)
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, k)
			b = append(b, ':')
			var err error
			if b, err = appendValue(b, t[k], depth+1); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	default:
		return nil, fmt.Errorf("%w: unsupported type %T", ErrInvalid, v)
	}
}

func appendInt(b []byte, n int64) ([]byte, error) {
	if n > MaxSafeInteger || n < -MaxSafeInteger {
		return nil, fmt.Errorf("%w: integer %d outside the safe range", ErrInvalid, n)
	}
	return strconv.AppendInt(b, n, 10), nil
}

const hexDigits = "0123456789abcdef"

func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\b':
			b = append(b, '\\', 'b')
		case '\t':
			b = append(b, '\\', 't')
		case '\n':
			b = append(b, '\\', 'n')
		case '\f':
			b = append(b, '\\', 'f')
		case '\r':
			b = append(b, '\\', 'r')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}

type parser struct {
	data []byte
	i    int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("%w at byte %d: %s", ErrInvalid, p.i, fmt.Sprintf(format, args...))
}

func (p *parser) ws() {
	for p.i < len(p.data) {
		switch p.data[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (Value, error) {
	if depth > maxDepth {
		return nil, p.errf("nesting deeper than %d", maxDepth)
	}
	if p.i >= len(p.data) {
		return nil, p.errf("unexpected end of input")
	}
	switch c := p.data[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == 't':
		return true, p.literal("true")
	case c == 'f':
		return false, p.literal("false")
	case c == 'n':
		return nil, p.literal("null")
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return nil, p.errf("unexpected character %q", c)
	}
}

func (p *parser) literal(word string) error {
	if len(p.data)-p.i < len(word) || string(p.data[p.i:p.i+len(word)]) != word {
		return p.errf("invalid literal")
	}
	p.i += len(word)
	return nil
}

func (p *parser) object(depth int) (Value, error) {
	p.i++ // {
	obj := Object{}
	p.ws()
	if p.i < len(p.data) && p.data[p.i] == '}' {
		p.i++
		return obj, nil
	}
	for {
		p.ws()
		if p.i >= len(p.data) || p.data[p.i] != '"' {
			return nil, p.errf("expected member name")
		}
		name, err := p.str()
		if err != nil {
			return nil, err
		}
		key := name.(string)
		if _, dup := obj[key]; dup {
			return nil, p.errf("duplicate member name %q", key)
		}
		p.ws()
		if p.i >= len(p.data) || p.data[p.i] != ':' {
			return nil, p.errf("expected ':'")
		}
		p.i++
		p.ws()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		obj[key] = v
		p.ws()
		if p.i >= len(p.data) {
			return nil, p.errf("unterminated object")
		}
		switch p.data[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return obj, nil
		default:
			return nil, p.errf("expected ',' or '}'")
		}
	}
}

func (p *parser) array(depth int) (Value, error) {
	p.i++ // [
	arr := []Value{}
	p.ws()
	if p.i < len(p.data) && p.data[p.i] == ']' {
		p.i++
		return arr, nil
	}
	for {
		p.ws()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		p.ws()
		if p.i >= len(p.data) {
			return nil, p.errf("unterminated array")
		}
		switch p.data[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return arr, nil
		default:
			return nil, p.errf("expected ',' or ']'")
		}
	}
}

func (p *parser) number() (Value, error) {
	start := p.i
	if p.data[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.data) || p.data[p.i] < '0' || p.data[p.i] > '9' {
		return nil, p.errf("invalid number")
	}
	if p.data[p.i] == '0' {
		p.i++
		if p.i < len(p.data) && p.data[p.i] >= '0' && p.data[p.i] <= '9' {
			return nil, p.errf("leading zero")
		}
	} else {
		for p.i < len(p.data) && p.data[p.i] >= '0' && p.data[p.i] <= '9' {
			p.i++
		}
	}
	if p.i < len(p.data) {
		switch p.data[p.i] {
		case '.', 'e', 'E':
			return nil, p.errf("fractional or exponent number")
		}
	}
	tok := string(p.data[start:p.i])
	if len(tok) > 17 { // "-" + 16 digits is the longest safe token
		return nil, p.errf("integer outside the safe range")
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil || n > MaxSafeInteger || n < -MaxSafeInteger {
		return nil, p.errf("integer outside the safe range")
	}
	return n, nil
}

func (p *parser) str() (Value, error) {
	p.i++ // opening quote
	var out []byte
	for {
		if p.i >= len(p.data) {
			return nil, p.errf("unterminated string")
		}
		c := p.data[p.i]
		switch {
		case c == '"':
			p.i++
			return string(out), nil
		case c == '\\':
			r, err := p.escape()
			if err != nil {
				return nil, err
			}
			out = utf8.AppendRune(out, r)
		case c < 0x20:
			return nil, p.errf("unescaped control character")
		case c < utf8.RuneSelf:
			out = append(out, c)
			p.i++
		default:
			r, size := utf8.DecodeRune(p.data[p.i:])
			if r == utf8.RuneError && size <= 1 {
				return nil, p.errf("invalid UTF-8")
			}
			out = append(out, p.data[p.i:p.i+size]...)
			p.i += size
		}
	}
}

// escape decodes one escape sequence at p.i (the backslash), joining a
// surrogate pair and rejecting a lone surrogate.
func (p *parser) escape() (rune, error) {
	if p.i+1 >= len(p.data) {
		return 0, p.errf("unterminated escape")
	}
	c := p.data[p.i+1]
	p.i += 2
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r1, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if !utf16.IsSurrogate(r1) {
			return r1, nil
		}
		if r1 >= 0xDC00 || p.i+1 >= len(p.data) || p.data[p.i] != '\\' || p.data[p.i+1] != 'u' {
			return 0, p.errf("lone surrogate")
		}
		p.i += 2
		r2, err := p.hex4()
		if err != nil {
			return 0, err
		}
		r := utf16.DecodeRune(r1, r2)
		if r == utf8.RuneError {
			return 0, p.errf("lone surrogate")
		}
		return r, nil
	default:
		return 0, p.errf("invalid escape")
	}
}

func (p *parser) hex4() (rune, error) {
	if len(p.data)-p.i < 4 {
		return 0, p.errf("short unicode escape")
	}
	var r rune
	for _, h := range p.data[p.i : p.i+4] {
		r <<= 4
		switch {
		case h >= '0' && h <= '9':
			r |= rune(h - '0')
		case h >= 'a' && h <= 'f':
			r |= rune(h-'a') + 10
		case h >= 'A' && h <= 'F':
			r |= rune(h-'A') + 10
		default:
			return 0, p.errf("invalid unicode escape")
		}
	}
	p.i += 4
	return r, nil
}
