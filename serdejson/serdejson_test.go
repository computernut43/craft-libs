package serdejson

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFormatFloats(t *testing.T) {
	// serde_json 1.0.151 (zmij) output, taken from the oracle.
	for f, want := range map[float32]string{
		0: "0.0", 1: "1.0", -1: "-1.0", 12.5: "12.5", 0.1: "0.1", 1e-7: "1e-7", 1.5e-6: "0.0000015",
		1e12: "1000000000000.0", 1e13: "1e+13", 1.25e12: "1250000000000.0", 3.4028235e38: "3.4028235e+38",
		1e-45: "1e-45", 72: "72.0", 16777216: "16777216.0", 0.33333334: "0.33333334",
	} {
		if got := FormatF32(f); got != want {
			t.Errorf("FormatF32(%v) = %s, want %s", f, got, want)
		}
	}
	if got := FormatF32(float32(math.Copysign(0, -1))); got != "-0.0" {
		t.Errorf("-0: %s", got)
	}
	for f, want := range map[float64]string{
		0.00001: "0.00001", 0.000001: "1e-6", 1e15: "1000000000000000.0", 1e16: "1e+16",
		-1.2072888614779534e-116: "-1.2072888614779534e-116", 3.836257962086347e163: "3.836257962086347e+163",
	} {
		if got := FormatF64(f); got != want {
			t.Errorf("FormatF64(%v) = %s, want %s", f, got, want)
		}
	}
	if FormatF32(float32(math.NaN())) != "null" || FormatF64(math.Inf(1)) != "null" {
		t.Fatal("NaN and infinities are null")
	}
}

func TestParseNumbers(t *testing.T) {
	if f, err := ParseF32("-0"); err != nil || f != 0 || !math.Signbit(float64(f)) {
		t.Fatalf("-0: %v %v", f, err)
	}
	if _, err := ParseF64("1e400"); err == nil {
		t.Fatal("1e400 is out of range")
	}
	if _, err := ParseF32("3.5e38"); err == nil {
		t.Fatal("3.5e38 is out of f32 range")
	}
	if f, err := ParseF64("1e-400"); err != nil || f != 0 {
		t.Fatalf("underflow: %v %v", f, err)
	}
	if f, err := ParseF32("16777217"); err != nil || f != 16777216 {
		t.Fatalf("integer rounding: %v %v", f, err)
	}
}

func TestPrettyEscapesAsSerdeJSON(t *testing.T) {
	got, err := Pretty([]byte(`{"a":"<&> \u0001\b\f\"\\/","b":[],"c":{},"d":[1,{"e":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": \"<&> \\u0001\\b\\f\\\"\\\\/\",\n  \"b\": [],\n  \"c\": {},\n  \"d\": [\n    1,\n    {\n      \"e\": null\n    }\n  ]\n}"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	compact, _ := Compact([]byte(`{ "a" : [ 1 , 2 ] }`))
	if string(compact) != `{"a":[1,2]}` {
		t.Fatal(string(compact))
	}
}

type withDefaults struct {
	Scale  F32    `json:"scale"`
	Name   string `json:"name"`
	Inner  inner  `json:"inner" serde:"default"`
	Option *bool  `json:"option"`
}

func (withDefaults) SerdeDefault() {}

func (w *withDefaults) SetDefaults() { w.Scale, w.Name = 100, "Normal" }

type inner struct {
	Pages int `json:"pages"`
}

func (i *inner) SetDefaults() { i.Pages = 1 }

type required struct {
	Inner inner `json:"inner" serde:"default"`
	N     int   `json:"n"`
}

func TestDefaults(t *testing.T) {
	var w withDefaults
	if err := Unmarshal([]byte(`{"name":"Title"}`), &w); err != nil {
		t.Fatal(err)
	}
	// Missing fields of a Defaulter take the struct's Default; the struct default decides inner.
	if w.Scale != 100 || w.Name != "Title" || w.Inner.Pages != 0 || w.Option != nil {
		t.Fatalf("%+v", w)
	}
	var r required
	if err := Unmarshal([]byte(`{"n":3}`), &r); err != nil || r.Inner.Pages != 1 {
		t.Fatalf("field default: %+v %v", r, err)
	}
	if err := Unmarshal([]byte(`{"inner":{"pages":2}}`), &r); err == nil || !strings.Contains(err.Error(), "missing field `n`") {
		t.Fatalf("got %v", err)
	}
	if err := Unmarshal([]byte(`[{"pages":2}]`), &r); err == nil {
		t.Fatal("a short sequence for a struct without defaults")
	}
	if err := Unmarshal([]byte(`[{"pages":2},5]`), &r); err != nil || r.N != 5 || r.Inner.Pages != 2 {
		t.Fatalf("sequence: %+v %v", r, err)
	}
}

// color mirrors a Rust enum with a hand-written Deserialize: "Auto" or {"Rgb": [r, g, b]}.
type color struct {
	auto bool
	rgb  [3]uint8
}

func (c *color) DecodeSerde(d Decoder) error {
	if d.Peek() == KindString {
		s, err := d.String()
		if err != nil {
			return err
		}
		if s != "Auto" {
			return errors.New("unknown variant")
		}
		c.auto = true
		return nil
	}
	return d.Object(func(k Key) error {
		if k.Name != "Rgb" {
			return errors.New("unknown variant")
		}
		return d.Decode(&c.rgb)
	})
}

func TestCustomDecoderAndRaw(t *testing.T) {
	var v struct {
		C    color  `json:"c"`
		Raw  rawVal `json:"raw"`
		Next int    `json:"next"`
	}
	if err := Unmarshal([]byte(`{"c":{"Rgb":[1,2,3]},"raw":{"x":[1,{"y":"\ud800"}]},"next":4}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.C.rgb != [3]uint8{1, 2, 3} || string(v.Raw.b) != `{"x":[1,{"y":"\ud800"}]}` || v.Next != 4 {
		t.Fatalf("%+v", v)
	}
	if err := Unmarshal([]byte(`{"c":"Red","raw":0,"next":1}`), &v); err == nil {
		t.Fatal("unknown variant accepted")
	}
}

type rawVal struct{ b []byte }

func (r *rawVal) DecodeSerde(d Decoder) error {
	b, err := d.Raw()
	r.b = append([]byte(nil), b...)
	return err
}

func TestHostileInputNeverPanics(t *testing.T) {
	for _, in := range []string{"", "{", `{"a":`, strings.Repeat("[", 100000), `{"unknown":` + strings.Repeat("[", 100000) + strings.Repeat("]", 100000) + `}`,
		`"\u`, `{"m":{"`, "\xff", `{"e":[1,2,3,4]}`, `{"m":{"-0":"x"}}`} {
		var v Strict
		_ = Unmarshal([]byte(in), &v)
	}
}

func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte(`{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"g":{},"h":{},"m":{"1":"x"},"bytes":[]}`))
	f.Add([]byte(`[1,"",null,[],[1,2,3],0.5,[],{},{},true,null,[]]`))
	f.Add([]byte(`{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"g":{},"h":{},"m":{},"bytes":[],"blocks":[{"kind":"para","under":{"single":{}},` +
		`"spacing":{"value":1.5,"rule":"atLeast"},"color":{"Rgb":[1,2,3]}},["other",1]],"color":"AUTO","spacing":["auto",null]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		var v Strict
		if Unmarshal(b, &v) != nil || hasNonFinite(reflect.ValueOf(v)) {
			// An f32 read from Content is the f64 rounded, which can overflow to infinity; serde
			// writes that as null, which doesn't read back (in Rust either).
			return
		}
		// What decodes writes back, and that reads again the same.
		out, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var w Strict
		if err := Unmarshal(out, &w); err != nil {
			t.Fatalf("%s doesn't read back: %v", out, err)
		}
		again, _ := Marshal(w)
		if string(again) != string(out) {
			t.Fatalf("not stable:\n%s\n%s", out, again)
		}
	})
}

func hasNonFinite(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return math.IsInf(v.Float(), 0) || math.IsNaN(v.Float())
	case reflect.Pointer:
		return !v.IsNil() && hasNonFinite(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if hasNonFinite(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if hasNonFinite(v.Index(i)) {
				return true
			}
		}
	}
	return false
}

// TestContentRules pins how serde reads the same value from JSON text, from buffered Content
// (inside an internally tagged block, or before an adjacently tagged enum's tag) and through a
// serde_json::Value. Each outcome was checked against serde_json 1.0.151 (TestMatchesRust's edge
// inputs).
func TestContentRules(t *testing.T) {
	const head = `{"a":1,"b":"","c":null,"d":[],"e":[1,2,3],"f":0,"g":{},"h":{},"m":{},"bytes":[]`
	for _, c := range []struct {
		field, val   string
		json, inPara bool // read at the top level; read inside a paragraph block
	}{
		{"under", `"single"`, true, true},
		{"under", `{"single":{}}`, false, true},
		{"spacing", `{"rule":{"auto":{}}}`, false, true},
		{"spacing", `[0,1.5]`, false, true},
		{"spacing", `{"value":1e39,"rule":"multiple"}`, true, true},
		{"spacing", `{"rule":"multiple","value":1e39}`, false, true},
		{"spacing", `{"rule":"auto","x":"\ud800"}`, true, false},
		{"spacing", `{"rule":"auto","value":{}}`, false, false},
		{"spacing", `{"value":1,"rule":"multiple","value":2}`, false, false},
		{"color", `"AuTo"`, true, true},
		{"color", `{"Auto":{}}`, false, false},
		{"color", `{"Rgb":[1,2,3],"Rgb":[4,5,6]}`, true, true},
		{"color", `{"Rgb":[1,2,3],"Rgb":[1e400]}`, false, false},
	} {
		var v Strict
		got := Unmarshal([]byte(head+`,"`+c.field+`":`+c.val+`}`), &v) == nil
		if got != c.json {
			t.Errorf("%s %s at the top level: read %v, want %v", c.field, c.val, got, c.json)
		}
		got = Unmarshal([]byte(head+`,"blocks":[{"kind":"para","`+c.field+`":`+c.val+`}]}`), &v) == nil
		if got != c.inPara {
			t.Errorf("%s %s in a block: read %v, want %v", c.field, c.val, got, c.inPara)
		}
	}
	// A Value keeps a repeated key's last value. An f32 read from JSON text is parsed straight
	// to f32, but one from Content is the f64 rounded: x is just above the midpoint between 1 and
	// the next f32, so straight to f32 it rounds up, while as an f64 it is the midpoint, which
	// then rounds to even (1).
	const x = "1.0000000596046447753906250000001"
	var v Strict
	if err := Unmarshal([]byte(head+`,"color":{"Rgb":[1,2,3],"Rgb":[4,5,6]},"spacing":{"rule":"multiple","value":`+x+`},`+
		`"blocks":[{"kind":"para","spacing":{"rule":"multiple","value":`+x+`}},{"n":7,"kind":"other"}]}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.Color == nil || v.Color.Rgb != [3]uint8{4, 5, 6} {
		t.Errorf("color = %+v", v.Color)
	}
	if len(v.Blocks) != 2 || v.Blocks[0].Para == nil || v.Blocks[0].Para.Spacing == nil || v.Blocks[1].Other == nil ||
		v.Blocks[1].Other.N != 7 || v.Spacing == nil {
		t.Fatalf("blocks = %+v, spacing = %+v", v.Blocks, v.Spacing)
	}
	if want := F32(math.Nextafter32(1, 2)); v.Spacing.Value != want {
		t.Errorf("spacing value from JSON = %v, want %v", v.Spacing.Value, want)
	}
	if got := v.Blocks[0].Para.Spacing.Value; got != 1 {
		t.Errorf("spacing value from Content = %v, want 1", got)
	}
	// Before the tag, the value is buffered as Content too.
	if err := Unmarshal([]byte(head+`,"spacing":{"value":`+x+`,"rule":"multiple"}}`), &v); err != nil || v.Spacing.Value != 1 {
		t.Errorf("spacing value before the tag = %+v (%v), want 1", v.Spacing, err)
	}
}
