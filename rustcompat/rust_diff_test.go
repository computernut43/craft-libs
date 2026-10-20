package rustcompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// EnvRust names the reference program built from testdata/rust-oracle; EnvCases how many random
// inputs per operation TestMatchesRust adds to the fixed ones (default 20,000).
const (
	EnvRust  = "CRAFT_RUSTCOMPAT_RUST"
	EnvCases = "CRAFT_RUSTCOMPAT_CASES"
)

// TestMatchesRust asks Rust's standard library and this package the same questions (number
// parsing and formatting, float min/max/clamp, casts, Euclidean division, per-character
// lowercase and whitespace for every code point, trimming) and compares the answers.
func TestMatchesRust(t *testing.T) {
	bin := os.Getenv(EnvRust)
	if bin == "" {
		t.Skipf("set %s to testdata/rust-oracle's release binary to compare with Rust", EnvRust)
	}
	if !filepath.IsAbs(bin) {
		t.Fatalf("%s must be absolute", EnvRust)
	}
	n := 20000
	if v := os.Getenv(EnvCases); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 {
			t.Fatalf("%s=%q: want a positive count", EnvCases, v)
		}
	}
	inputs := cases(rand.New(rand.NewPCG(7, 3)), n)
	var in bytes.Buffer
	for _, x := range inputs {
		in.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(x))))
		in.WriteString(x)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin, cmd.Env = &in, []string{}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != len(inputs) {
		t.Fatalf("reference answered %d of %d", len(lines), len(inputs))
	}
	bad := 0
	perOp := map[byte]int{}
	for i, x := range inputs {
		perOp[x[0]]++
		got, want := answer(x[0], x[1:]), lines[i]
		if x[0] == 'm' && runtime.GOARCH != "amd64" {
			// Which of 0 and -0 Rust's max and min return when they are equal is the CPU's
			// choice; the Go code follows x86-64, so elsewhere the sign of zero isn't compared.
			got = strings.ReplaceAll(got, "8000000000000000", "0000000000000000")
			want = strings.ReplaceAll(want, "8000000000000000", "0000000000000000")
		}
		if got != want {
			bad++
			if bad <= 10 {
				t.Errorf("%c %q:\nrust: %s\ngo:   %s", x[0], x[1:], lines[i], got)
			}
		}
	}
	t.Logf("%d inputs (per operation %v), %d differ", len(inputs), perOp, bad)
	if bad > 0 {
		t.Fatalf("%d of %d differ", bad, len(inputs))
	}
}

func f64Of(s string) float64 {
	u, _ := strconv.ParseUint(s, 16, 64)
	return math.Float64frombits(u)
}

func bitsOf(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	return fmt.Sprintf("%016x", math.Float64bits(f))
}

func hexBytes(s string) string {
	var b strings.Builder
	for i := range len(s) {
		fmt.Fprintf(&b, "%02x", s[i])
	}
	return b.String()
}

// answer is what the reference program prints for one input.
func answer(op byte, arg string) string {
	parts := strings.Split(arg, ":")
	part := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	afterColon := func() (string, string) {
		b, s, _ := strings.Cut(arg, ":")
		return b, s
	}
	switch op {
	case 'f':
		if f, ok := ParseF64(arg); ok {
			return bitsOf(f)
		}
		return "ERR"
	case 'g':
		if f, ok := ParseF32(arg); ok {
			if f != f {
				return "NaN"
			}
			return fmt.Sprintf("%08x", math.Float32bits(f))
		}
		return "ERR"
	case 'i':
		b, s := afterColon()
		bits, _ := strconv.Atoi(b)
		if bits == 0 {
			bits = 64
		}
		if v, ok := ParseInt(s, bits); ok {
			return strconv.FormatInt(v, 10)
		}
		return "ERR"
	case 'u':
		b, s := afterColon()
		bits, _ := strconv.Atoi(b)
		if bits == 0 {
			bits = 64
		}
		if v, ok := ParseUint(s, bits); ok {
			return strconv.FormatUint(v, 10)
		}
		return "ERR"
	case 'd':
		return Display(f64Of(arg))
	case 'D':
		u, _ := strconv.ParseUint(arg, 16, 32)
		return Display32(math.Float32frombits(uint32(u)))
	case 'x':
		p, _ := strconv.Atoi(part(1))
		return Fixed(f64Of(part(0)), p)
	case 'e':
		p, _ := strconv.Atoi(part(1))
		return Exp(f64Of(part(0)), p)
	case 'E':
		return LowerExp(f64Of(arg))
	case 'm':
		a, b, c := f64Of(part(0)), f64Of(part(1)), f64Of(part(2))
		lo, hi := c, b
		if b <= c {
			lo, hi = b, c
		}
		clamp := "skip"
		if lo == lo && hi == hi {
			clamp = bitsOf(Clamp(a, lo, hi))
		}
		return bitsOf(Max(a, b)) + " " + bitsOf(Min(a, b)) + " " + clamp
	case 'c':
		f := f64Of(arg)
		return fmt.Sprintf("%d %d %d %d", I64(f), I32(f), U64(f), U32(f))
	case 'r':
		a, _ := strconv.ParseInt(part(0), 10, 64)
		b, err := strconv.ParseInt(part(1), 10, 64)
		if err != nil {
			b = 1
		}
		if b == 0 || (a == math.MinInt64 && b == -1) {
			return "skip"
		}
		return fmt.Sprintf("%d %d", DivEuclid(a, b), RemEuclid(a, b))
	case 'l':
		u, _ := strconv.ParseUint(arg, 16, 32)
		c := rune(u)
		if !utf8.ValidRune(c) {
			c = '?'
		}
		var low []string
		for _, l := range CharToLowercase(nil, c) {
			low = append(low, strconv.FormatInt(int64(l), 16))
		}
		return strings.Join(low, ",") + " " + strconv.FormatBool(IsWhitespace(c))
	case 't':
		return hexBytes(Trim(arg)) + " " + hexBytes(TrimStart(arg)) + " " + hexBytes(TrimEnd(arg))
	}
	return "BAD OP"
}

var floatEdges = []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 0.5, 1.5, 2.5, -2.5, 0.125, 0.375, 1e15, 1e16, 1e21, 1e22, 1e-5, 1e-7, 1e-300,
	5e-324, math.SmallestNonzeroFloat64, math.MaxFloat64, -math.MaxFloat64, math.Inf(1), math.Inf(-1), math.NaN(), 123456789012345.6,
	1234567890123456789, 0.1 + 0.2, 1.0 / 3, 2.0 / 3, 9.999999999999999e22, 999.9999999999999, 1000.0000000000001, 9223372036854775807,
	-9223372036854775808, 9223372036854775808, 18446744073709551615, 18446744073709551616, 4294967295, 4294967296, -2147483649, 2147483647.9,
	-0.99, 0.99, 1e300}

func randFloat(rng *rand.Rand) float64 {
	switch rng.IntN(5) {
	case 0:
		return floatEdges[rng.IntN(len(floatEdges))]
	case 1:
		return math.Float64frombits(rng.Uint64())
	case 2:
		// Decimal-looking values: k/10^j, the halfway cases of fixed formatting.
		return float64(rng.Int64N(2000000)-1000000) / math.Pow(10, float64(rng.IntN(8)))
	case 3:
		return rng.NormFloat64() * math.Pow(10, float64(rng.IntN(60)-30))
	}
	return float64(rng.Int64N(1<<53)) * math.Pow(2, float64(rng.IntN(200)-100))
}

func randNumberText(rng *rand.Rand) string {
	pieces := []string{"", "+", "-", "0", "1", "12", "007", ".", ".5", "5.", "e", "E", "e5", "e+5", "e-5", "e+", "inf", "Inf", "INFINITY", "nan",
		"NaN", "infinit", " ", "_", "1_0", "0x10", "1e400", "1e-400", "2.2250738585072011e-308", "4.9e-324", "1.7976931348623157e308",
		"1.7976931348623159e308", "99999999999999999999", "0.30000000000000004", "١", "１", "--1", "+-1", "1e", "1.e5", ".e5"}
	var b strings.Builder
	for range 1 + rng.IntN(3) {
		b.WriteString(pieces[rng.IntN(len(pieces))])
	}
	if rng.IntN(3) == 0 {
		return strconv.FormatFloat(randFloat(rng), "fegx"[rng.IntN(3)], -1, 64)
	}
	return b.String()
}

func randIntText(rng *rand.Rand) string {
	pieces := []string{"", "+", "-", "0", "1", "9", "127", "128", "-128", "-129", "255", "256", "32767", "65535", "2147483647", "2147483648",
		"4294967295", "4294967296", "9223372036854775807", "9223372036854775808", "18446744073709551615", "18446744073709551616", "_", " ", "1e3",
		"0x1", "٣"}
	var b strings.Builder
	for range 1 + rng.IntN(2) {
		b.WriteString(pieces[rng.IntN(len(pieces))])
	}
	return b.String()
}

func cases(rng *rand.Rand, n int) []string {
	var out []string
	add := func(op byte, arg string) { out = append(out, string(op)+arg) }
	h := func(f float64) string { return fmt.Sprintf("%016x", math.Float64bits(f)) }
	for _, f := range floatEdges {
		add('d', h(f))
		add('E', h(f))
		add('c', h(f))
		for p := range 20 {
			add('x', h(f)+":"+strconv.Itoa(p))
			add('e', h(f)+":"+strconv.Itoa(p))
		}
	}
	// Every code point: lowercase and whitespace.
	for c := rune(0); c <= 0x10FFFF; c++ {
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		add('l', strconv.FormatInt(int64(c), 16))
	}
	ws := []string{" ", "\t", "\n", "\u00a0", "\u2028", "\u3000", "\u200b", "\ufeff", "\u0085", "\u180e", "x", "é"}
	for range 2000 {
		var b strings.Builder
		for range rng.IntN(6) {
			b.WriteString(ws[rng.IntN(len(ws))])
		}
		add('t', b.String())
	}
	for range n {
		add('f', randNumberText(rng))
		add('g', randNumberText(rng))
		bits := []string{"8", "16", "32", "64"}[rng.IntN(4)]
		add('i', bits+":"+randIntText(rng))
		add('u', bits+":"+randIntText(rng))
		add('d', h(randFloat(rng)))
		add('D', fmt.Sprintf("%08x", rng.Uint32()))
		add('x', h(randFloat(rng))+":"+strconv.Itoa(rng.IntN(31)))
		add('e', h(randFloat(rng))+":"+strconv.Itoa(rng.IntN(20)))
		add('E', h(randFloat(rng)))
		add('m', h(randFloat(rng))+":"+h(randFloat(rng))+":"+h(randFloat(rng)))
		add('c', h(randFloat(rng)))
		add('r', strconv.FormatInt(rng.Int64()>>uint(rng.IntN(64)), 10)+":"+strconv.FormatInt((rng.Int64()>>uint(rng.IntN(64)))-int64(rng.IntN(3)), 10))
	}
	return out
}
