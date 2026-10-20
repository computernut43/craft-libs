package serdejson

import (
	"bytes"
	"sort"
	"strconv"
)

// Value is a JSON value read as serde_json::Value reads one (without preserve_order): numbers
// keep whether they were an integer (u64 or i64) or a float, and an object keeps one entry per
// key (the last), in key order.
type Value struct {
	Kind   Kind
	Bool   bool
	Str    string
	Num    string // the literal: an integer, or a float written back in serde_json's form
	IsInt  bool   // Num fits u64 or i64 (serde_json's PosInt/NegInt)
	Arr    []Value
	Keys   []string // sorted
	Fields map[string]Value
}

// Value reads the next value as serde_json::Value::deserialize does: strings checked, numbers
// parsed (a float out of range is an error), nesting counted.
func (d *jsonDecoder) Value() (Value, error) {
	c, err := d.Content()
	if err != nil {
		return Value{}, err
	}
	return contentValue(&c), nil
}

func sortStrings(s []string) { sort.Strings(s) }

// JSON writes the value back as JSON (compact), so it can be decoded into a type: serde's
// from_value. Integers stay integers and floats stay floats.
func (v Value) JSON() []byte {
	var b bytes.Buffer
	v.write(&b)
	return b.Bytes()
}

func (v Value) write(b *bytes.Buffer) {
	switch v.Kind {
	case KindBool:
		b.WriteString(strconv.FormatBool(v.Bool))
	case KindNumber:
		b.WriteString(v.Num)
	case KindString:
		writeString(b, v.Str)
	case KindArray:
		b.WriteByte('[')
		for i, e := range v.Arr {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	case KindObject:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			v.Fields[k].write(b)
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// FromValue decodes a Value into v as serde_json::from_value does: no recursion limit, numbers
// as the Value holds them (an f32 from a float is the f64 rounded).
func FromValue(val Value, v any) error {
	c := valueContent(val)
	return (&contentDecoder{cur: &c, fromValue: true}).Decode(v)
}

func valueContent(v Value) Content {
	switch v.Kind {
	case KindBool:
		return Content{Kind: KindBool, Bool: v.Bool}
	case KindNumber:
		c, err := numberContent(v.Num, &contentDecoder{})
		if err != nil {
			return Content{Kind: KindNull}
		}
		return c
	case KindString:
		return Content{Kind: KindString, Str: v.Str}
	case KindArray:
		c := Content{Kind: KindArray}
		for _, e := range v.Arr {
			c.Seq = append(c.Seq, valueContent(e))
		}
		return c
	case KindObject:
		c := Content{Kind: KindObject}
		for _, k := range v.Keys {
			c.Keys = append(c.Keys, k)
			c.Vals = append(c.Vals, valueContent(v.Fields[k]))
		}
		return c
	}
	return Content{Kind: KindNull}
}
