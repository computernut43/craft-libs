package serdejson

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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
	// Read through serde's Content (internally and adjacently tagged enums) and Value.
	Blocks  Vec[Block] `json:"blocks" serde:"default"`
	Color   *Color     `json:"color,omitempty" serde:"default"`
	Under   *Under     `json:"under,omitempty" serde:"default"`
	Spacing *Spacing   `json:"spacing,omitempty" serde:"default"`
}

// Block is internally tagged on "kind": its fields are buffered as Content and read from there.
type Block struct {
	Para  *ParaLike
	Other *Other
	Break bool
}

type Other struct {
	N uint32 `json:"n"`
}

func (b *Block) DecodeSerde(d Decoder) error {
	i, rest, err := d.Tagged("kind", []string{"para", "other", "break"})
	if err != nil {
		return err
	}
	*b = Block{}
	if i == 2 {
		b.Break = true
		return TaggedUnit(rest)
	}
	if i == 0 {
		b.Para = new(ParaLike)
		return rest.Decode(b.Para)
	}
	b.Other = new(Other)
	return rest.Decode(b.Other)
}

// MarshalJSON writes the tag first, then the variant's fields.
func (b Block) MarshalJSON() ([]byte, error) {
	if b.Break {
		return []byte(`{"kind":"break"}`), nil
	}
	kind, fields := "other", any(b.Other)
	if b.Para != nil {
		kind, fields = "para", b.Para
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	out := []byte(`{"kind":"` + kind + `"`)
	if rest := raw[1:]; len(rest) > 1 {
		out = append(out, ',')
		out = append(out, rest...)
		return out, nil
	}
	return append(out, '}'), nil
}

type ParaLike struct {
	Text    string   `json:"text"`
	Size    *F32     `json:"size,omitempty"`
	Color   *Color   `json:"color,omitempty"`
	Under   *Under   `json:"under,omitempty"`
	Spacing *Spacing `json:"spacing,omitempty"`
	List    Vec[F32] `json:"list"`
	Inner   Inner    `json:"inner"`
}

func (ParaLike) SerdeDefault() {}

type Under uint8

var underNames = []string{"none", "single", "dotDash"}

func (u *Under) DecodeSerde(d Decoder) error {
	i, err := d.UnitVariant(underNames)
	*u = Under(i)
	return err
}

func (u Under) MarshalJSON() ([]byte, error) { return json.Marshal(underNames[u]) }

// Spacing is adjacently tagged ("rule", "value"); auto has no value.
type Spacing struct {
	Rule  uint8
	Value F32
}

var spacingRules = []string{"multiple", "atLeast", "auto"}

func (s *Spacing) DecodeSerde(d Decoder) error {
	*s = Spacing{}
	i, err := AdjacentlyTagged(d, "rule", "value", spacingRules, func(i int) bool { return i == 2 }, func(i int, d Decoder) error {
		if i == 2 {
			if null, err := d.Null(); null || err != nil {
				return err
			}
			return d.Fail("invalid type, expected unit")
		}
		f, err := d.Float(32)
		s.Value = F32(f)
		return err
	})
	s.Rule = uint8(i)
	return err
}

func (s Spacing) MarshalJSON() ([]byte, error) {
	if s.Rule == 2 {
		return []byte(`{"rule":"auto"}`), nil
	}
	return []byte(`{"rule":"` + spacingRules[s.Rule] + `","value":` + FormatF32(float32(s.Value)) + "}"), nil
}

// Color reads a serde_json::Value first: "auto" in any ASCII case, else the externally tagged
// form.
type Color struct {
	Auto bool
	Rgb  [3]uint8
}

func (c *Color) DecodeSerde(d Decoder) error {
	v, err := d.Value()
	if err != nil {
		return err
	}
	if v.Kind == KindString {
		if !asciiEqualFold(v.Str, "auto") {
			return d.Fail("bad colour")
		}
		*c = Color{Auto: true}
		return nil
	}
	var w writtenColor
	if err := FromValue(v, &w); err != nil {
		return d.Fail(err.Error())
	}
	*c = Color(w)
	return nil
}

type writtenColor Color

func (w *writtenColor) DecodeSerde(d Decoder) error {
	*w = writtenColor{}
	i, err := ExternallyTagged(d, []string{"Auto", "Rgb"}, func(i int) bool { return i == 0 }, func(_ int, d Decoder) error {
		return d.Decode(&w.Rgb)
	})
	w.Auto = i == 0
	return err
}

func (c Color) MarshalJSON() ([]byte, error) {
	if c.Auto {
		return []byte(`"Auto"`), nil
	}
	return json.Marshal(map[string][3]uint8{"Rgb": c.Rgb})
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
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
	inputs := edgeInputs()
	for range n {
		inputs = append(inputs, randomInput(rng))
	}
	n = len(inputs)
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
// seqRequired are Options without a default: they may be missing from a map, not from a
// sequence.
var seqRequired = map[string]bool{"c": true, "o": true}

var nullable = map[string]bool{"size": true, "boldCs": true, "fontName": true, "c": true, "o": true, "color": true, "under": true,
	"spacing": true}

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
		g.seq(fields, depth)
		return
	}
	g.fields(fields, depth)
}

// seq writes a struct as an array of its fields in order. Fields after the last one serde
// requires there (one without a default) may be left off.
func (g *gen) seq(fields []fieldGen, depth int) {
	g.b.WriteByte('[')
	required := 0
	for i, f := range fields {
		if !f.optional || seqRequired[f.name] {
			required = i + 1
		}
	}
	n := required + g.rng.IntN(len(fields)-required+1)
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
}

// fields writes a struct as a map.
func (g *gen) fields(fields []fieldGen, depth int) {
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
		{"blocks", func(g *gen, d int) { g.array(func(g *gen, d int) { g.block(d) }, d) }, true},
		{"color", func(g *gen, _ int) { g.color() }, true},
		{"under", func(g *gen, _ int) { g.under() }, true},
		{"spacing", func(g *gen, _ int) { g.spacing() }, true},
	}
	g.object(fields, 0)
}

func (g *gen) paraFields() []fieldGen {
	return []fieldGen{
		{"text", func(g *gen, _ int) { g.b.WriteString(g.str()) }, true},
		{"size", func(g *gen, _ int) { g.b.WriteString(g.number("f32")) }, true},
		{"color", func(g *gen, _ int) { g.color() }, true},
		{"under", func(g *gen, _ int) { g.under() }, true},
		{"spacing", func(g *gen, _ int) { g.spacing() }, true},
		{"list", func(g *gen, d int) { g.array(func(g *gen, _ int) { g.b.WriteString(g.number("f32")) }, d) }, true},
		{"inner", func(g *gen, d int) { g.object(g.innerFields(), d) }, true},
	}
}

// block writes an internally tagged Block: the tag anywhere among the fields of a map, or first
// in a sequence. serde reads it all into Content first, so every value in it (even an unknown
// field's) is read in full, and its fields are then decoded with Content's rules.
func (g *gen) block(depth int) {
	if g.oops(30) {
		g.b.WriteString(g.pick(`"para"`, "null", "1", "{}", "[]"))
		return
	}
	kind, fields := `"para"`, g.paraFields()
	switch g.rng.IntN(6) {
	case 0, 1:
		kind, fields = `"other"`, []fieldGen{{"n", func(g *gen, _ int) { g.b.WriteString(g.number("u32")) }, false}}
	case 2:
		// A unit variant: a map may hold anything else, a sequence nothing.
		kind = `"break"`
		fields = []fieldGen{{"x", func(g *gen, _ int) { g.b.WriteString(g.pick("1", `"\ud800"`, "[]")) }, true}}
		if !g.oops(2) {
			if g.chance(8) {
				g.b.WriteString(`["break"]`)
				return
			}
			fields = nil
		}
	}
	if g.oops(10) {
		// From JSON the tag is a name only; an index or a unit-variant map isn't one.
		kind = g.pick(`"Para"`, `"x"`, `""`, "0", "1", `{"para":null}`, "null", `"\u0070ara"`)
	}
	tag := fieldGen{"kind", func(g *gen, _ int) { g.b.WriteString(kind) }, false}
	if g.chance(8) {
		g.seq(append([]fieldGen{tag}, fields...), depth)
		return
	}
	at := g.rng.IntN(len(fields) + 1)
	all := append(append(append([]fieldGen(nil), fields[:at]...), tag), fields[at:]...)
	g.fields(all, depth)
}

// color writes a Color, which is read through serde_json::Value.
func (g *gen) color() {
	if g.oops(4) {
		g.b.WriteString(g.pick(`"red"`, `"Rgb"`, `"aut"`, `"autö"`, `{"Rgb":[1,2]}`, `{"Rgb":[1,2,3,4]}`, `{"Rgb":[256,0,0]}`,
			`{"Rgb":[1.0,2,3]}`, `{"Rgb":[-0,1,2]}`, `{"Rgb":[-1,1,2]}`, `{"Auto":{}}`, `{"Auto":null,"Rgb":[1,2,3]}`, `{}`,
			`{"rgb":[1,2,3]}`, `[1,2,3]`, `{"Auto":1}`, `{"Rgb":[1,2,3],"Rgb":[1e400]}`, `{"Rgb":null}`, `{"Auto":"x"}`, "true", "1",
			`{"Rgb":{"0":1}}`, `{"Rgb":"\ud800"}`, `{"Auto":null}x`))
		return
	}
	switch g.rng.IntN(4) {
	case 0:
		g.b.WriteString(g.pick(`"auto"`, `"AUTO"`, `"aUtO"`, `"Auto"`, `"\u0061uto"`))
	case 1:
		g.b.WriteString(g.pick(`{"Auto":null}`, `{"\u0041uto" : null}`, `{"Rgb":[1,2,3],"Rgb":[4,5,6]}`, `{"Auto":null,"Auto":null}`))
	default:
		g.b.WriteString(`{"Rgb":[` + g.number("u8") + "," + g.number("u8") + " , " + g.number("u8") + "]}")
	}
}

// under writes an externally tagged unit enum.
func (g *gen) under() {
	if g.oops(4) {
		// {"single":{}} is a unit only in Content.
		g.b.WriteString(g.pick(`"dotdash"`, `"Single"`, `{"single":{}}`, `{"single":null,"none":null}`, `{}`, "0", "1",
			`{"single":1}`, `["single"]`, `{"single":[]}`, `{"bogus":null}`))
		return
	}
	g.b.WriteString(g.pick(`"none"`, `"single"`, `"dotDash"`, `{"single":null}`, `{ "dotDash" : null }`, `"\u0073ingle"`))
}

// spacing writes an adjacently tagged Spacing. Content before the tag is buffered and read with
// Content's rules (an f32 is then the f64 rounded); after the tag it's read straight from JSON.
func (g *gen) spacing() {
	v := g.number("f32")
	if g.oops(3) {
		g.b.WriteString(g.pick(`{"rule":"auto","value":1}`, `{"rule":"multiple"}`, `{"value":1}`, `{}`,
			`{"rule":"multiple","value":1,"rule":"auto"}`, `{"rule":"multiple","value":1,"value":2}`,
			`{"value":1,"value":2,"rule":"multiple"}`, `{"value":1,"rule":"multiple","value":2}`,
			`{"value":1,"rule":"multiple","rule":"auto"}`, `{"rule":{"auto":{}}}`, `["auto"]`, `["auto",null,1]`, `[0,1.5]`,
			`[2,null]`, `[]`, `["multiple"]`, `{"rule":0,"value":1}`, `{"rule":"multiple","value":"1"}`, `{"rule":"Multiple","value":1}`,
			`{"rule":"multiple","value":1e39}`, `{"value":1e39,"rule":"multiple"}`, `{"value":1e400,"rule":"multiple"}`,
			`{"rule":"auto","value":{}}`, `{"value":{},"rule":"auto"}`, `{"rule":{"multiple":null,"auto":null},"value":1}`,
			`{"rule":{},"value":1}`, `"auto"`, `{"rule":null}`, `{"value":"\ud800","rule":"auto"}`, `{"rule":"auto","x":"\ud800"}`,
			`{"value":-0,"rule":"multiple"}`, `{"rule":"multiple","value":-0}`, `{"value":16777217,"rule":"atLeast"}`,
			`["multiple",1e39]`, `{"rule":"auto","value":null,"rule":"auto"}`, `{"value":null,"value":null,"rule":"auto"}`))
		return
	}
	g.b.WriteString(g.pick(
		`{"rule":"multiple","value":`+v+`}`,
		`{"value":`+v+`,"rule":"atLeast"}`,
		`{"rule":"auto"}`,
		`{"rule":"auto","value":null}`,
		`{"value":null,"rule":"auto"}`,
		`["multiple",`+v+`]`,
		`["auto",null]`,
		`{"x":1,"rule":"multiple","y":[{}],"value":`+v+`,"z":null}`,
		`{"rule":{"atLeast":null},"value":`+v+`}`,
		`{"value":`+v+`,"rule":{"auto":null}}`,
		`{"value":`+v+`,"x":"\ud800","rule":"multiple"}`,
	))
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

// edgeValues are values for the Content-read fields whose outcome depends on where they're read
// from: JSON text, buffered Content (inside a block), or a serde_json::Value.
var edgeValues = map[string][]string{
	"under": {`"single"`, `{"single":null}`, `{"single":{}}`, `{"single":[]}`, `{"single":null,"none":null}`, `0`, `{}`, `"\u0073ingle"`},
	"spacing": {`{"rule":"auto","value":null}`, `{"rule":"auto","value":{}}`, `{"value":{},"rule":"auto"}`, `{"rule":{"auto":{}}}`,
		`{"rule":{"auto":null}}`, `[0,1.5]`, `[2,null]`, `["auto",null]`, `["auto",{}]`, `["multiple",1.5,2]`, `{"rule":0,"value":1}`,
		`{"value":1e39,"rule":"multiple"}`, `{"rule":"multiple","value":1e39}`, `{"value":1.0000000596046447753906250000001,"rule":"multiple"}`,
		`{"rule":"multiple","value":1.0000000596046447753906250000001}`, `{"value":-0,"rule":"atLeast"}`, `{"value":16777217,"rule":"atLeast"}`,
		`{"value":1,"value":2,"rule":"multiple"}`, `{"value":1,"rule":"multiple","value":2}`, `{"rule":"multiple","value":1,"rule":"auto"}`,
		`{"value":"\ud800","rule":"auto"}`, `{"rule":"auto","x":"\ud800"}`, `{"rule":"auto","value":null,"x":{}}`, `{"x":[],"rule":"auto"}`},
	"color": {`"AuTo"`, `"Auto"`, `"red"`, `{"Auto":null}`, `{"Auto":{}}`, `{"Rgb":[1,2,3]}`, `{"Rgb":[1,2,3],"Rgb":[4,5,6]}`,
		`{"Rgb":[1,2,3],"Rgb":[1e400]}`, `{"Rgb":[1.0,2,3]}`, `{"Rgb":[-0,2,3]}`, `{"Auto":null,"Rgb":[1,2,3]}`, `{"Rgb":[1,2]}`, `[1,2,3]`,
		`{"Rgb":[1,2,3],"x":1}`, `{"\u0052gb":[1,2,3]}`, `{"Rgb":[1,2,"3"]}`},
	"size": {`1e39`, `-1e39`, `1e-46`, `1.0000000596046447753906250000001`, `16777217`, `-0`, `1e400`},
}

// edgeInputs places each edge value at the top level (read from JSON) and in a paragraph block
// (read from Content), and adds blocks whose tag or shape is unusual.
func edgeInputs() [][]byte {
	const head = `{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"g":{},"h":{},"m":{},"bytes":[]`
	var out [][]byte
	for _, field := range []string{"under", "spacing", "color", "size"} {
		for _, v := range edgeValues[field] {
			if field != "size" {
				out = append(out, []byte(head+`,"`+field+`":`+v+`}`))
			} else {
				out = append(out, []byte(head+`,"c":`+v+`}`))
			}
			out = append(out, []byte(head+`,"blocks":[{"kind":"para","`+field+`":`+v+`}]}`))
			if field == "size" {
				out = append(out, []byte(head+`,"blocks":[{"`+field+`":`+v+`,"kind":"para","list":[`+v+`]}]}`))
			}
		}
	}
	for _, b := range []string{`["para"]`, `["para","t",1.5]`, `["para","t",1.5,"auto","single",["auto",null],[1],{},1]`, `["other",7]`,
		`["other"]`, `[0,7]`, `{"kind":1,"n":7}`, `{"kind":"other","n":7,"kind":"other"}`, `{"n":7,"kind":"other","x":"\ud800"}`,
		`{"n":7,"kind":"other","x":"\u00e9"}`, `{"kind":"other","n":7.0}`, `{"kind":"other","n":-0}`, `{"kind":"other","n":4294967296}`,
		`{"kind":"other"}`, `{"kind":"para","inner":[1.5,true]}`, `{"kind":"para","inner":{"m":{"1":1}}}`, `{"kind":"para","text":null}`,
		`{"kind":"para","size":null}`, `{"kind":"para","inner":{"nested":[{"size":1e39}]}}`, `{"kind":{"para":null}}`, `{"kind":"\u0070ara"}`,
		// A unit variant: anything else in a map, nothing else in a sequence.
		`{"kind":"break"}`, `{"kind":"break","x":[1]}`, `{"x":"\u00e9","kind":"break"}`, `["break"]`, `["break",1]`, `["break",null]`,
		`{"kind":"break","x":"\ud800"}`} {
		out = append(out, []byte(head+`,"blocks":[`+b+`]}`))
	}
	return out
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
