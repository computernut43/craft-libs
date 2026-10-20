// Package serdejson reads and writes JSON the way Rust's serde_json (1.0.151, float_roundtrip)
// does, for Go ports of Rust data models whose files must stay interchangeable with the Rust
// apps' — field for field, and byte for byte where the Rust app wrote them.
//
// Reading: Unmarshal is a decoder of its own, driven by the Go type as serde's derived
// Deserialize is driven by the Rust type. Keys match exactly; a struct field given twice is an
// error; unknown keys are skipped (without depth limit, checked only for syntax); null is only
// for pointers (Options); a field must be present unless it is a pointer, the struct is a
// Defaulter or the field is tagged `serde:"default"` (DefaultSetter supplies a non-zero
// default); a struct may also be given as the sequence of its fields; fixed arrays need their
// exact length; integer and map-key literals follow serde_json's integer rules; floats are
// parsed as serde_json parses them (ParseF32, ParseF64); decoded strings must be UTF-8 with
// paired surrogate escapes; decoded containers nest at most RecursionLimit deep. Types with a
// hand-written Deserialize implement SerdeDecoder.
//
// Writing: encoding/json encodes (struct fields in declaration order, as serde does) and
// Marshal/Pretty/Compact rewrite the result in serde_json's layout and string escaping. F32 and
// F64 write floats as serde_json does (zmij's shortest form: "12.0", "0.001234", "1.5e+20");
// Vec, StrMap and U32Map write empty collections, bytes and numeric keys as serde_json does.
package serdejson

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// FormatF32 writes f as serde_json writes an f32 (zmij, which serde_json 1.0.151 formats
// with). NaN and infinities have no JSON form: serde_json writes null for them, and so does F32.
func FormatF32(f float32) string { return format(float64(f), 32) }

// FormatF64 writes f as serde_json writes an f64.
func FormatF64(f float64) string { return format(f, 64) }

// format writes the shortest digits that read back as f, laid out as zmij lays them out: plain
// for decimal exponents in -6..=12 (f32) or -5..=15 (f64) ("12.0", "0.001234"), otherwise
// scientific with a signed exponent ("1.5e+20", "1e-7").
func format(f float64, bits int) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	var b strings.Builder
	if math.Signbit(f) {
		b.WriteByte('-')
	}
	if f == 0 {
		b.WriteString("0.0")
		return b.String()
	}
	e := strconv.FormatFloat(math.Abs(f), 'e', -1, bits)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr) // value = d.ddd × 10^exp
	lo, hi := -6, 12
	if bits == 64 {
		lo, hi = -5, 15
	}
	length := len(digits)
	switch {
	case exp >= lo && exp <= hi && length-1 <= exp:
		// 1234e7 -> 12340000000.0
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", exp-(length-1)))
		b.WriteString(".0")
	case exp >= lo && exp <= hi && exp >= 0:
		// 1234e-2 -> 12.34
		b.WriteString(digits[:exp+1])
		b.WriteByte('.')
		b.WriteString(digits[exp+1:])
	case exp >= lo && exp <= hi:
		// 1234e-6 -> 0.001234
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -exp-1))
		b.WriteString(digits)
	default:
		// 1234e30 -> 1.234e+33
		b.WriteString(digits[:1])
		if length > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if exp >= 0 {
			b.WriteByte('+')
		} else {
			b.WriteByte('-')
		}
		if exp < 0 {
			exp = -exp
		}
		b.WriteString(strconv.Itoa(exp))
	}
	return b.String()
}

var errNumber = errors.New("serdejson: invalid number")

// isInteger reports a JSON number literal without fraction or exponent, which serde_json reads as
// an integer (u64 or i64) when it fits.
func isInteger(lit string) bool { return !strings.ContainsAny(lit, ".eE") }

// errRange is a number serde_json refuses as out of range (its float parse gave infinity).
var errRange = errors.New("serdejson: number out of range")

// ParseF64 reads a JSON number literal as serde_json (float_roundtrip) reads an f64: an integer
// literal that fits u64 is converted from it; a negative one from i64 when it fits, "-0" as -0.0,
// a larger one from its magnitude as f64; anything else is parsed correctly rounded. A value
// beyond the f64 range is an error, as in serde_json.
func ParseF64(lit string) (float64, error) {
	if v, ok := integerLiteral(lit); ok {
		return v, nil
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, errNumber
	}
	if math.IsInf(f, 0) {
		return 0, errRange
	}
	return f, nil
}

// ParseF32 reads a JSON number literal as serde_json (float_roundtrip) reads an f32: integer
// literals are converted as for ParseF64 and then to f32, anything else is parsed straight to a
// correctly rounded f32. A value beyond the f32 range is an error.
func ParseF32(lit string) (float32, error) {
	if isInteger(lit) {
		neg := strings.HasPrefix(lit, "-")
		if mag, err := strconv.ParseUint(strings.TrimPrefix(lit, "-"), 10, 64); err == nil {
			switch {
			case !neg:
				return float32(mag), nil
			case mag == 0:
				return float32(math.Copysign(0, -1)), nil
			case mag <= 1<<63:
				return float32(-int64(mag-1) - 1), nil
			}
			// serde_json keeps it as the f64 -(mag as f64); then `as f32`.
			return float32(-float64(mag)), nil
		}
	}
	f, err := strconv.ParseFloat(lit, 32)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, errNumber
	}
	if math.IsInf(f, 0) {
		return 0, errRange
	}
	return float32(f), nil
}

// integerLiteral converts an integer literal that fits u64 (by magnitude) as serde_json does
// for an f64; ok is false for other literals.
func integerLiteral(lit string) (float64, bool) {
	if !isInteger(lit) {
		return 0, false
	}
	neg := strings.HasPrefix(lit, "-")
	mag, err := strconv.ParseUint(strings.TrimPrefix(lit, "-"), 10, 64)
	if err != nil {
		return 0, false
	}
	switch {
	case !neg:
		return float64(mag), true
	case mag == 0:
		return math.Copysign(0, -1), true
	case mag <= 1<<63:
		return float64(-int64(mag-1) - 1), true
	}
	return -float64(mag), true
}

// F32 is an f32 field: written as serde_json writes an f32 (Unmarshal reads any float32 as
// serde_json reads an f32).
type F32 float32

// MarshalJSON writes the number as serde_json does (null for NaN and infinities).
func (f F32) MarshalJSON() ([]byte, error) { return []byte(FormatF32(float32(f))), nil }

// F64 is an f64 field: written as serde_json writes an f64.
type F64 float64

// MarshalJSON writes the number as serde_json does (null for NaN and infinities).
func (f F64) MarshalJSON() ([]byte, error) { return []byte(FormatF64(float64(f))), nil }
