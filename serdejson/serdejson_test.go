package serdejson

import (
	"errors"
	"math"
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

func (c *color) DecodeSerde(d *Decoder) error {
	if d.Peek() == '"' {
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
	return d.Object(func(key string) error {
		if key != "Rgb" {
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

func (r *rawVal) DecodeSerde(d *Decoder) error {
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
	f.Fuzz(func(t *testing.T, b []byte) {
		var v Strict
		if Unmarshal(b, &v) != nil {
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
