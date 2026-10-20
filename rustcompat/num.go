// Package rustcompat gives Go ports of the Craft apps the Rust standard library's semantics where
// Go's differ: parsing and formatting numbers (`str::parse`, `format!("{}")`, `{:.N}`, `{:e}`),
// f32/f64 min, max and clamp around NaN, saturating float-to-integer casts, Euclidean division,
// and Unicode case mapping per character. Each function is compared with Rust's own std by the
// reference program in testdata/rust-oracle (TestMatchesRust).
package rustcompat

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Float is f32 or f64.
type Float interface{ ~float32 | ~float64 }

// ParseF64 is `s.parse::<f64>()`: an optional sign, then `inf`, `infinity` or `nan` (any case),
// or decimal digits with an optional point and an optional exponent (at least one digit, and
// digits after the `e`). No spaces, underscores or hex. Out of range is ±inf or ±0, as in Rust.
func ParseF64(s string) (float64, bool) { return parseFloat(s, 64) }

// ParseF32 is `s.parse::<f32>()` (rounded straight to f32, as Rust does).
func ParseF32(s string) (float32, bool) {
	f, ok := parseFloat(s, 32)
	return float32(f), ok
}

func parseFloat(s string, bits int) (float64, bool) {
	body := s
	neg := false
	if body != "" && (body[0] == '+' || body[0] == '-') {
		neg = body[0] == '-'
		body = body[1:]
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		if neg {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	if !isDecimal(body) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, bits)
	if err != nil {
		// Out of range: strconv gives ±Inf or ±0 with ErrRange, which is Rust's answer.
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			return 0, false
		}
	}
	return f, true
}

// isDecimal reports Rust's decimal float grammar: digits [. digits] or . digits, then an optional
// exponent [eE][+-]digits.
func isDecimal(s string) bool {
	i, digits := 0, 0
	for i < len(s) && isDigit(s[i]) {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		start := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(s)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// ParseInt is `s.parse::<iN>()` for bits = N: an optional sign, then at least one ASCII digit,
// in range.
func ParseInt(s string, bits int) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, bits)
	return n, err == nil
}

// ParseUint is `s.parse::<uN>()` for bits = N: an optional `+` (a `-` is an invalid digit, even
// in "-0"), then at least one ASCII digit, in range.
func ParseUint(s string, bits int) (uint64, bool) {
	t := strings.TrimPrefix(s, "+")
	if t == "" || t[0] == '+' || t[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseUint(t, 10, bits)
	return n, err == nil
}

// special is Rust's text for NaN and the infinities, and false for a finite number.
func special(f float64) (string, bool) {
	switch {
	case math.IsNaN(f):
		return "NaN", true
	case math.IsInf(f, 1):
		return "inf", true
	case math.IsInf(f, -1):
		return "-inf", true
	}
	return "", false
}

// Display is `format!("{}", f)` for an f64: the shortest digits that read back as f, never in
// exponent form ("1e21" is written out in full, -0.0 is "-0").
func Display(f float64) string {
	if s, ok := special(f); ok {
		return s
	}
	return plain(shortest(f, 64))
}

// Display32 is `format!("{}", f)` for an f32.
func Display32(f float32) string {
	if s, ok := special(float64(f)); ok {
		return s
	}
	return plain(shortest(float64(f), 32))
}

// decimal is a sign, significant digits and the exponent of the first digit (d.ddd × 10^exp).
type decimal struct {
	neg    bool
	digits string
	exp    int
}

// shortest is the shortest decimal that reads back as f (bits wide). When two such decimals are
// equally close to f, Rust takes the one further from zero where strconv takes the one with the
// even last digit.
func shortest(f float64, bits int) decimal {
	s := strconv.FormatFloat(f, 'e', -1, bits)
	d := decimal{neg: strings.HasPrefix(s, "-")}
	s = strings.TrimPrefix(s, "-")
	mant, exp, _ := strings.Cut(s, "e")
	d.digits = strings.Replace(mant, ".", "", 1)
	d.exp, _ = strconv.Atoi(exp)
	up, ok := d.next()
	if !ok {
		return d
	}
	// The candidate one unit further from zero must read back as f too, and be exactly as far
	// from it.
	if g, err := strconv.ParseFloat(up.text(), bits); err != nil || g != f {
		return d
	}
	x := new(big.Rat)
	if bits == 32 {
		x.SetFloat64(float64(float32(f)))
	} else {
		x.SetFloat64(f)
	}
	lo, _ := new(big.Rat).SetString(d.text())
	hi, _ := new(big.Rat).SetString(up.text())
	if lo == nil || hi == nil {
		return d
	}
	if new(big.Rat).Sub(x, lo).Abs(new(big.Rat).Sub(x, lo)).Cmp(new(big.Rat).Sub(hi, x).Abs(new(big.Rat).Sub(hi, x))) == 0 {
		return up
	}
	return d
}

// next is d with its last digit one higher in magnitude (carrying), or false for no digits.
func (d decimal) next() (decimal, bool) {
	if d.digits == "" {
		return d, false
	}
	b := []byte(d.digits)
	i := len(b) - 1
	for i >= 0 && b[i] == '9' {
		b[i] = '0'
		i--
	}
	if i < 0 {
		return decimal{neg: d.neg, digits: "1" + strings.TrimRight(string(b), "0"), exp: d.exp + 1}, true
	}
	b[i]++
	return decimal{neg: d.neg, digits: strings.TrimRight(string(b), "0"), exp: d.exp}, true
}

// text is the decimal in exponent form, as strconv reads it.
func (d decimal) text() string {
	sign := ""
	if d.neg {
		sign = "-"
	}
	digits := d.digits
	if digits == "" {
		digits = "0"
	}
	return sign + digits[:1] + "." + digits[1:] + "0e" + strconv.Itoa(d.exp)
}

// plain writes the decimal without an exponent, as Rust's `{}` does.
func plain(d decimal) string {
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	digits := d.digits
	switch {
	case d.exp < 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -d.exp-1))
		b.WriteString(digits)
	case d.exp+1 >= len(digits):
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", d.exp+1-len(digits)))
	default:
		b.WriteString(digits[:d.exp+1])
		b.WriteByte('.')
		b.WriteString(digits[d.exp+1:])
	}
	return b.String()
}

// Fixed is `format!("{:.prec$}", f)`: exactly prec decimals, the exact value rounded half to even.
func Fixed(f float64, prec int) string {
	if s, ok := special(f); ok {
		return s
	}
	return strconv.FormatFloat(f, 'f', max(prec, 0), 64)
}

// Exp is `format!("{:.prec$e}", f)`: one digit, a point and prec digits when prec > 0, then `e`
// and the exponent without a plus sign or padding ("1.50e3", "2e-7").
func Exp(f float64, prec int) string {
	if s, ok := special(f); ok {
		return s
	}
	return rustExponent(strconv.FormatFloat(f, 'e', max(prec, 0), 64))
}

// LowerExp is `format!("{:e}", f)`: the shortest digits in exponent form ("1e3", "1.5e-7").
func LowerExp(f float64) string {
	if s, ok := special(f); ok {
		return s
	}
	d := shortest(f, 64)
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	b.WriteString(d.digits[:1])
	if len(d.digits) > 1 {
		b.WriteByte('.')
		b.WriteString(d.digits[1:])
	}
	b.WriteString("e" + strconv.Itoa(d.exp))
	return b.String()
}

// rustExponent turns strconv's "1.5e+03" into Rust's "1.5e3".
func rustExponent(s string) string {
	mant, exp, ok := strings.Cut(s, "e")
	if !ok {
		return s
	}
	neg := strings.HasPrefix(exp, "-")
	exp = strings.TrimLeft(strings.TrimLeft(exp, "+-"), "0")
	if exp == "" {
		exp = "0"
	}
	if neg {
		exp = "-" + exp
	}
	return mant + "e" + exp
}

// Max is Rust's `a.max(b)`: when one is NaN, the other; when they are equal (0 and -0), b, as
// the baseline's x86-64 build does.
func Max[F Float](a, b F) F {
	switch {
	case a != a:
		return b
	case b != b || a > b:
		return a
	}
	return b
}

// Min is Rust's `a.min(b)`: when one is NaN, the other; when they are equal (0 and -0), b.
func Min[F Float](a, b F) F {
	switch {
	case a != a:
		return b
	case b != b || a < b:
		return a
	}
	return b
}

// Clamp is Rust's `v.clamp(lo, hi)` (lo <= hi): NaN stays NaN, and v is kept when it equals a
// bound (so -0 clamped to [0, 1] stays -0).
func Clamp[F Float](v, lo, hi F) F {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}

// I64 is Rust's `f as i64`: truncation toward zero, NaN is 0, out of range saturates.
func I64[F Float](f F) int64 {
	v := float64(f)
	switch {
	case v != v:
		return 0
	case v >= 9223372036854775807:
		return math.MaxInt64
	case v <= -9223372036854775808:
		return math.MinInt64
	}
	return int64(v)
}

// I32 is Rust's `f as i32`.
func I32[F Float](f F) int32 {
	v := float64(f)
	switch {
	case v != v:
		return 0
	case v >= math.MaxInt32:
		return math.MaxInt32
	case v <= math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}

// U64 is Rust's `f as u64` (and `as usize` on 64-bit targets): NaN and negatives are 0, out of
// range saturates.
func U64[F Float](f F) uint64 {
	v := float64(f)
	switch {
	case v != v || v <= 0:
		return 0
	case v >= 18446744073709551615:
		return math.MaxUint64
	}
	return uint64(v)
}

// U32 is Rust's `f as u32`.
func U32[F Float](f F) uint32 {
	v := float64(f)
	switch {
	case v != v || v <= 0:
		return 0
	case v >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(v)
}

// DivEuclid is Rust's `a.div_euclid(b)` for integers (b != 0; the caller handles MIN / -1).
func DivEuclid(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		if b > 0 {
			q--
		} else {
			q++
		}
	}
	return q
}

// RemEuclid is Rust's `a.rem_euclid(b)` for integers: never negative.
func RemEuclid(a, b int64) int64 {
	r := a % b
	if r < 0 {
		if b < 0 {
			r -= b
		} else {
			r += b
		}
	}
	return r
}
