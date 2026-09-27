package decision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize returns the canonical JSON form of v (SPEC §2.1): RFC 8785 (JCS) with object keys
// sorted by UTF-16 code units, ES6 number formatting, no insignificant whitespace and no HTML
// escaping. Two additions to RFC 8785 are normative for TrustAgent: U+2028 and U+2029 are escaped
// as \u2028 and \u2029, and invalid UTF-8 (the Go form of a lone surrogate) becomes U+FFFD.
//
// v may be a tree of nil, bool, string, float64, float32, the integer types, json.Number,
// json.RawMessage, []any and map[string]any. Any other value is first converted with
// encoding/json, so structs canonicalize through their JSON tags. NaN and infinities are errors.
func Canonicalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalizeJSON parses a JSON document and returns its canonical form.
func CanonicalizeJSON(doc []byte) ([]byte, error) {
	v, err := decodeGeneric(doc)
	if err != nil {
		return nil, err
	}
	return Canonicalize(v)
}

// SHA256Hex returns the lowercase hex SHA-256 of the canonical form of v.
func SHA256Hex(v any) (string, error) {
	b, err := Canonicalize(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeCanonicalString(buf, x)
	case float64:
		return writeCanonicalNumber(buf, x)
	case float32:
		return writeCanonicalNumber(buf, float64(x))
	case int:
		return writeCanonicalNumber(buf, float64(x))
	case int64:
		return writeCanonicalNumber(buf, float64(x))
	case int32:
		return writeCanonicalNumber(buf, float64(x))
	case uint:
		return writeCanonicalNumber(buf, float64(x))
	case uint64:
		return writeCanonicalNumber(buf, float64(x))
	case uint32:
		return writeCanonicalNumber(buf, float64(x))
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return fmt.Errorf("canonical json: number %q: %w", x.String(), err)
		}
		return writeCanonicalNumber(buf, f)
	case json.RawMessage:
		g, err := decodeGeneric(x)
		if err != nil {
			return err
		}
		return writeCanonical(buf, g)
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		SortUTF16(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		g, err := toGeneric(v)
		if err != nil {
			return err
		}
		return writeCanonical(buf, g)
	}
	return nil
}

// toGeneric converts an arbitrary value (struct, typed map or slice) to the generic JSON tree
// through encoding/json.
func toGeneric(v any) (any, error) {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return decodeGeneric(b)
}

// decodeGeneric parses JSON into nil, bool, string, float64, []any and map[string]any. The
// decoder already maps lone surrogate escapes to U+FFFD.
func decodeGeneric(doc []byte) (any, error) {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return v, nil
}

// writeCanonicalNumber formats f as ES6 Number.prototype.toString does (RFC 8785 §3.2.2.3):
// shortest round-trip digits, plain notation for 1e-6 <= |f| < 1e21, otherwise exponent notation
// with an explicit sign and no leading zeros ("1e+21", "1e-7"). -0 is written as 0.
func writeCanonicalNumber(buf *bytes.Buffer, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("canonical json: %v is not a JSON number", f)
	}
	if f == 0 {
		buf.WriteByte('0')
		return nil
	}
	format := byte('f')
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	b := strconv.AppendFloat(nil, f, format, -1, 64)
	if format == 'e' {
		// Go writes a two-digit exponent ("1e-07"); ES6 writes the shortest ("1e-7").
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	buf.Write(b)
	return nil
}

const hexDigits = "0123456789abcdef"

// U+2028 and U+2029 are valid in JSON strings but not in JavaScript source, so SPEC §2.1 escapes them.
const (
	lineSeparator      = 0x2028
	paragraphSeparator = 0x2029
)

// writeCanonicalString writes s as a JSON string the way ES6 JSON.stringify does, plus the
// U+2028/U+2029 escapes, with invalid UTF-8 replaced by U+FFFD.
func writeCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r < 0x20:
			buf.WriteString(`\u00`)
			buf.WriteByte(hexDigits[r>>4])
			buf.WriteByte(hexDigits[r&0xf])
		case r == lineSeparator:
			buf.WriteString(`\u2028`)
		case r == paragraphSeparator:
			buf.WriteString(`\u2029`)
		default:
			// utf8.RuneError with size 1 is an invalid byte; WriteRune writes U+FFFD for it.
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// SortUTF16 sorts strings by their UTF-16 code units, the key order of RFC 8785 and of
// JavaScript's default sort. It differs from Go's byte order for characters above U+FFFF, whose
// surrogates (U+D800..U+DFFF) sort before U+E000..U+FFFF.
func SortUTF16(keys []string) {
	slices.SortFunc(keys, CompareUTF16)
}

// CompareUTF16 compares a and b by UTF-16 code units (invalid UTF-8 counts as U+FFFD).
func CompareUTF16(a, b string) int {
	if isASCII(a) && isASCII(b) {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
