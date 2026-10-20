package serdejson

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Strict and Inner mirror the oracle's Rust types (testdata/rust-oracle/src/main.rs).
type Strict struct {
	A     uint32         `json:"a"`
	B     string         `json:"b"`
	C     *F32           `json:"c"`
	D     Vec[int64]     `json:"d"`
	E     [3]uint8       `json:"e"`
	F     F64            `json:"f"`
	G     Inner          `json:"g"`
	H     StrMap[uint32] `json:"h"`
	M     U32Map[string] `json:"m"`
	X     bool           `json:"x" serde:"default"`
	O     *Inner         `json:"o,omitempty"`
	Bytes Vec[uint8]     `json:"bytes"`
}

type Inner struct {
	Size     *F32       `json:"size,omitempty"`
	BoldCs   *bool      `json:"boldCs"`
	FontName *string    `json:"fontName"`
	List     Vec[F32]   `json:"list"`
	Nested   Vec[Inner] `json:"nested"`
	N        int32      `json:"n"`
}

func (Inner) SerdeDefault() {}

// EnvRust names the reference program built from testdata/rust-oracle; EnvCases how many inputs
// TestMatchesRust compares (default 30,000).
const (
	EnvRust  = "CRAFT_SERDEJSON_RUST"
	EnvCases = "CRAFT_SERDEJSON_CASES"
)

// TestMatchesRust reads random JSON (valid documents of the test types, documents with every
// kind of mistake serde and encoding/json treat differently, and byte-level damage) into the
// test types with Unmarshal and with serde_json, and compares what each writes back (Marshal and
// to_string_pretty), or that both fail.
func TestMatchesRust(t *testing.T) {
	bin := os.Getenv(EnvRust)
	if bin == "" {
		t.Skipf("set %s to testdata/rust-oracle's release binary to compare with Rust", EnvRust)
	}
	if !filepath.IsAbs(bin) {
		t.Fatalf("%s must be absolute", EnvRust)
	}
	n := 30000
	if v := os.Getenv(EnvCases); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 {
			t.Fatalf("%s=%q: want a positive count", EnvCases, v)
		}
	}
	rng := rand.New(rand.NewPCG(2, 9))
	inputs := make([][]byte, n)
	for i := range inputs {
		inputs[i] = randomInput(rng)
	}
	var in bytes.Buffer
	for _, x := range inputs {
		in.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(x))))
		in.Write(x)
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
	if len(lines) != n {
		t.Fatalf("reference answered %d of %d", len(lines), n)
	}
	bad, ok := 0, 0
	for i, x := range inputs {
		got := goAnswer(x)
		if got != lines[i] {
			bad++
			if bad <= 5 {
				t.Errorf("input %d %q:\nrust: %s\ngo:   %s", i, x, lines[i], got)
			}
		} else if got != "ERROR" {
			ok++
		}
	}
	t.Logf("%d inputs, %d read, %d refused by both, %d differ", n, ok, n-ok-bad, bad)
	if bad > 0 {
		t.Fatalf("%d of %d differ", bad, n)
	}
}

func goAnswer(x []byte) string {
	var v Strict
	if err := Unmarshal(x, &v); err != nil {
		return "ERROR"
	}
	b, err := Marshal(v)
	if err != nil {
		return "MARSHAL ERROR: " + err.Error()
	}
	return strings.ReplaceAll(strings.ReplaceAll(string(b), `\`, `\\`), "\n", `\n`)
}

// gen writes random JSON for the test types.
type gen struct {
	rng *rand.Rand
	b   strings.Builder
	// hostile inputs make one or two mistakes (so each mistake decides the outcome); the others
	// are valid documents with varied values.
	hostile  bool
	mistakes int
}

func (g *gen) chance(n int) bool { return g.rng.IntN(n) == 0 }

// oops is a mistake, made only in hostile inputs and only while their budget lasts.
func (g *gen) oops(n int) bool {
	if !g.hostile || g.mistakes == 0 || !g.chance(n) {
		return false
	}
	g.mistakes--
	return true
}

func (g *gen) pick(xs ...string) string { return xs[g.rng.IntN(len(xs))] }

var f32Cases = []float32{0, 1, -1, 12, 12.5, 0.1, 1e-7, 1.5e-6, 123456789, 1e13, 1.25e12, 3.4028235e38, 1.1754944e-38, 1e-45,
	float32(math.Copysign(0, -1)), 72, 0.333333343, 16777217}

func (g *gen) number(kind string) string {
	switch {
	case g.oops(8):
		return g.pick("0", "-0", "1", "-1", "4294967295", "4294967296", "-2147483649", "18446744073709551615", "18446744073709551616",
			"1.0", "1e2", "1E+2", "-0.0", "0.1", "1.5", "2.5e-3", "1e400", "-1e400", "123456789012345678901234567890", "0.30000000000000004",
			"16777217", "9007199254740993", "3.4028235677973366e38", "1e-46", "7.006492321624085e-46")
	case kind == "f32" && g.chance(4):
		// A long decimal: parsed straight to f32 it can round differently than through f64.
		f := math.Float64frombits(g.rng.Uint64()>>2 | 0x3800000000000000)
		return strconv.FormatFloat(f*float64(1-2*g.rng.IntN(2)), 'g', 17+g.rng.IntN(4), 64)
	case kind == "f32":
		f := f32Cases[g.rng.IntN(len(f32Cases))]
		if g.chance(2) {
			f = math.Float32frombits(g.rng.Uint32())
			for math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				f = math.Float32frombits(g.rng.Uint32())
			}
		}
		return FormatF32(f)
	case kind == "f64":
		if g.chance(2) {
			f := math.Float64frombits(g.rng.Uint64())
			if math.IsNaN(f) || math.IsInf(f, 0) {
				f = 0.5
			}
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
		return FormatF64(g.rng.NormFloat64() * math.Pow(10, float64(g.rng.IntN(40)-20)))
	case kind == "u8":
		return strconv.Itoa(g.rng.IntN(256))
	case kind == "u32":
		return strconv.FormatUint(uint64(g.rng.Uint32()), 10)
	case kind == "i32":
		return strconv.FormatInt(int64(int32(g.rng.Uint32())), 10)
	}
	return strconv.FormatInt(int64(g.rng.Uint64()), 10)
}

func (g *gen) str() string {
	parts := []string{"a", "b c", `\"`, `\\`, `\/`, `\n`, `\t`, `\b`, `\f`, `\r`, `\u0001`, `\u001f`, `\u00e9`, "é", "😀", `\ud83d\ude00`,
		"\u2028", "<&>"}
	if g.oops(5) {
		parts = append(parts, `\ud800`, `\udc00`, `\ud800x`, `\ud800\u0041`, `\x`)
	}
	var s strings.Builder
	s.WriteByte('"')
	for range g.rng.IntN(4) {
		s.WriteString(parts[g.rng.IntN(len(parts))])
	}
	if g.oops(30) {
		s.WriteByte(g.pick("\x00", "\x01", "\x1f", "\x7f")[0])
	}
	if g.oops(30) {
		s.WriteString(g.pick("\xc3", "\xff", "\xed\xa0\x80"))
	}
	s.WriteByte('"')
	return s.String()
}

// key writes an object key: usually the field's own name, sometimes a variant serde ignores
// (and encoding/json would take).
func (g *gen) key(name string) string {
	switch {
	case g.chance(40):
		// serde ignores it (encoding/json alone would take it); a required field then goes
		// missing, which is an error only in hostile inputs' sense.
		if g.oops(1) {
			return `"` + strings.ToUpper(name) + `"`
		}
	case g.oops(40):
		return `"` + name + ` "`
	}
	return `"` + name + `"`
}

// nullable are the fields that are Options (null is None); a default only lets a field be
// missing.
var nullable = map[string]bool{"size": true, "boldCs": true, "fontName": true, "c": true, "o": true}

type fieldGen struct {
	name     string
	val      func(*gen, int)
	optional bool
}

func (g *gen) object(fields []fieldGen, depth int) {
	if depth > 3 {
		g.b.WriteString("{}")
		return
	}
	if g.oops(30) {
		g.b.WriteString(g.pick("null", "[]", "1", `"s"`, "{}"))
		return
	}
	if g.chance(25) {
		// A struct as an array of its fields in order.
		g.b.WriteByte('[')
		n := len(fields)
		if fields[len(fields)-1].optional {
			n = g.rng.IntN(len(fields) + 1)
		}
		if g.oops(5) {
			n = g.rng.IntN(len(fields) + 2)
		}
		for i := 0; i < n; i++ {
			if i > 0 {
				g.b.WriteByte(',')
			}
			if i < len(fields) {
				fields[i].val(g, depth+1)
			} else {
				g.b.WriteString("0")
			}
		}
		g.b.WriteByte(']')
		return
	}
	g.b.WriteByte('{')
	first := true
	for _, f := range fields {
		if f.optional && g.chance(5) || !f.optional && g.oops(20) {
			continue // missing
		}
		reps := 1
		if g.oops(40) {
			reps = 2 // duplicate
		}
		for range reps {
			if !first {
				g.b.WriteByte(',')
			}
			first = false
			g.b.WriteString(g.key(f.name))
			g.b.WriteString(g.pick(":", " : "))
			if (nullable[f.name] && g.chance(10)) || g.oops(40) {
				g.b.WriteString("null")
			} else {
				f.val(g, depth+1)
			}
		}
	}
	if g.chance(6) {
		if !first {
			g.b.WriteByte(',')
		}
		if g.chance(4) {
			// Ignored, but still within serde_json's recursion limit (or just past it):
			// the object and this key's value are two levels already.
			d := RecursionLimit - 3 + g.rng.IntN(5)
			g.b.WriteString(`"unknown":` + strings.Repeat("[", d) + strings.Repeat("]", d))
		} else {
			// Skipped: lone surrogates and bad UTF-8 are fine there, control characters aren't.
			junk := g.pick(`\ud800`, "\xff", `\udc00x`, "é")
			if g.oops(3) {
				junk = g.pick("\x1f", "\x00", `\x`, `\u12`)
			}
			g.b.WriteString(`"unknown":{"deep":[1,2,{"x":null,"Y":"` + junk + `"}]}`)
		}
	}
	g.b.WriteByte('}')
}

func (g *gen) array(elem func(*gen, int), depth int) {
	if g.oops(40) {
		g.b.WriteString(g.pick("null", "{}", `"x"`))
		return
	}
	g.b.WriteByte('[')
	for i := range g.rng.IntN(4) {
		if i > 0 {
			g.b.WriteByte(',')
		}
		elem(g, depth+1)
	}
	g.b.WriteByte(']')
}

func (g *gen) innerFields() []fieldGen {
	return []fieldGen{
		{"size", func(g *gen, _ int) { g.b.WriteString(g.number("f32")) }, true},
		{"boldCs", func(g *gen, _ int) {
			if g.oops(5) {
				g.b.WriteString(g.pick("1", `"true"`))
				return
			}
			g.b.WriteString(g.pick("true", "false"))
		}, true},
		{"fontName", func(g *gen, _ int) { g.b.WriteString(g.str()) }, true},
		{"list", func(g *gen, d int) { g.array(func(g *gen, _ int) { g.b.WriteString(g.number("f32")) }, d) }, true},
		{"nested", func(g *gen, d int) { g.array(func(g *gen, d int) { g.object(g.innerFields(), d) }, d) }, true},
		{"n", func(g *gen, _ int) { g.b.WriteString(g.number("i32")) }, true},
	}
}

func (g *gen) strict() {
	fields := []fieldGen{
		{"a", func(g *gen, _ int) { g.b.WriteString(g.number("u32")) }, false},
		{"b", func(g *gen, _ int) { g.b.WriteString(g.str()) }, false},
		{"c", func(g *gen, _ int) { g.b.WriteString(g.number("f32")) }, true},
		{"d", func(g *gen, d int) { g.array(func(g *gen, _ int) { g.b.WriteString(g.number("int")) }, d) }, false},
		{"e", func(g *gen, _ int) {
			g.b.WriteByte('[')
			n := 3
			if g.oops(8) {
				n = g.rng.IntN(5)
			}
			for i := range n {
				if i > 0 {
					g.b.WriteByte(',')
				}
				g.b.WriteString(g.number("u8"))
			}
			g.b.WriteByte(']')
		}, false},
		{"f", func(g *gen, _ int) { g.b.WriteString(g.number("f64")) }, false},
		{"g", func(g *gen, d int) { g.object(g.innerFields(), d) }, false},
		{"h", func(g *gen, _ int) {
			g.b.WriteByte('{')
			for i := range g.rng.IntN(4) {
				if i > 0 {
					g.b.WriteByte(',')
				}
				g.b.WriteString(g.pick(`"k"`, `"a"`, `"Z"`, `"k"`, `""`) + ":" + g.number("u32"))
			}
			g.b.WriteByte('}')
		}, false},
		{"m", func(g *gen, _ int) {
			g.b.WriteByte('{')
			for i := range g.rng.IntN(4) {
				if i > 0 {
					g.b.WriteByte(',')
				}
				k := g.pick(`"1"`, `"2"`, `"10"`, `"0"`, `"4294967295"`)
				if g.oops(5) {
					k = g.pick(`"01"`, `"-1"`, `"1.0"`, `" 1"`, `"4294967296"`, `"\u0031"`, `"x"`)
				}
				g.b.WriteString(k + ":" + g.str())
			}
			g.b.WriteByte('}')
		}, false},
		{"x", func(g *gen, _ int) { g.b.WriteString(g.pick("true", "false")) }, true},
		{"o", func(g *gen, d int) { g.object(g.innerFields(), d) }, true},
		{"bytes", func(g *gen, d int) {
			if g.oops(10) {
				g.b.WriteString(`"AAEC"`) // base64, which encoding/json would take for []byte
				return
			}
			g.array(func(g *gen, _ int) { g.b.WriteString(g.number("u8")) }, d)
		}, false},
	}
	g.object(fields, 0)
}

func randomInput(rng *rand.Rand) []byte {
	g := &gen{rng: rng, hostile: rng.IntN(5) < 2, mistakes: 1 + rng.IntN(2)}
	if g.chance(30) {
		return deepInput(RecursionLimit - 3 + g.rng.IntN(7))
	}
	if g.chance(40) {
		// Nesting at serde_json's recursion limit.
		depth := 120 + g.rng.IntN(16)
		return []byte(`{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"h":{},"m":{},"bytes":[],"g":{"nested":` +
			strings.Repeat(`[{"nested":`, depth/2) + "[]" + strings.Repeat("}]", depth/2) + "}}")
	}
	g.strict()
	out := []byte(g.b.String())
	if g.oops(3) {
		for range 1 + g.rng.IntN(2) {
			if len(out) == 0 {
				break
			}
			i := g.rng.IntN(len(out))
			switch g.rng.IntN(3) {
			case 0:
				out = out[:i]
			case 1:
				const damage = "{}[],:\"\\ 0-.e\xff"
				out[i] = damage[g.rng.IntN(len(damage))]
			default:
				out = append(out[:i:i], append([]byte(g.pick(",", " ", "\t", "\n", "0", "]")), out[i:]...)...)
			}
		}
	}
	if g.chance(20) {
		out = append(out, " \n"...)
	}
	if g.oops(10) {
		out = append(out, g.pick(" x", "{}", ",")...)
	}
	return out
}

func TestGeneratorMakesValidInput(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 9))
	ok := 0
	for range 3000 {
		var v Strict
		if Unmarshal(randomInput(rng), &v) == nil {
			ok++
		}
	}
	t.Logf("%d of 3000 read", ok)
	if ok < 150 {
		t.Fatalf("only %d of 3000 generated inputs read: the comparison would prove little", ok)
	}
}

// deepInput is a valid Strict whose deepest decoded container is at depth exactly depth: Strict
// (1), g (2), then g's "nested" arrays and their Inners in turn, the last one empty.
func deepInput(depth int) []byte {
	var b strings.Builder
	b.WriteString(`{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"h":{},"m":{},"bytes":[],"g":`)
	level := 2
	b.WriteString("{")
	closers := "}"
	for level < depth {
		if level%2 == 0 {
			b.WriteString(`"nested":[`)
			closers = "]" + closers
		} else {
			b.WriteString("{")
			closers = "}" + closers
		}
		level++
	}
	b.WriteString(closers)
	b.WriteString("}")
	return []byte(b.String())
}
