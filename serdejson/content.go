package serdejson

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strconv"
)

// Content is a value as serde buffers one (serde's private Content, as serde_json fills it):
// integers that fit u64 (or, negative, i64) stay integers, other numbers are f64; maps keep their
// entries in order, repeats included.
type Content struct {
	Kind Kind
	Bool bool
	Str  string
	// Number: U64 or I64 when Int, else F.
	Int  bool
	Neg  bool // I64 (negative)
	U64  uint64
	I64  int64
	F    float64
	Seq  []Content
	Keys []string // map keys, in order
	Vals []Content
}

// Content reads the next value into Content, as serde_json's deserialize_any does: nesting
// counted, strings checked, numbers parsed (a float out of f64 range is an error).
func (d *jsonDecoder) Content() (Content, error) {
	switch d.Peek() {
	case KindObject:
		c := Content{Kind: KindObject}
		err := d.Object(func(k Key) error {
			v, err := d.Content()
			c.Keys = append(c.Keys, k.Name)
			c.Vals = append(c.Vals, v)
			return err
		})
		return c, err
	case KindArray:
		c := Content{Kind: KindArray}
		err := d.Array(func(int) error {
			v, err := d.Content()
			c.Seq = append(c.Seq, v)
			return err
		})
		return c, err
	case KindString:
		s, err := d.String()
		return Content{Kind: KindString, Str: s}, err
	case KindBool:
		b, err := d.Bool()
		return Content{Kind: KindBool, Bool: b}, err
	case KindNull:
		_, err := d.Null()
		return Content{Kind: KindNull}, err
	case KindNumber:
		lit, err := d.number()
		if err != nil {
			return Content{}, err
		}
		return numberContent(lit, d)
	}
	return Content{}, d.typeError("a value")
}

// numberContent is a literal as serde_json's parse_any_number gives it.
func numberContent(lit string, d Decoder) (Content, error) {
	if isInteger(lit) && lit != "-0" {
		if lit[0] == '-' {
			if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
				return Content{Kind: KindNumber, Int: true, Neg: true, I64: n}, nil
			}
		} else if n, err := strconv.ParseUint(lit, 10, 64); err == nil {
			return Content{Kind: KindNumber, Int: true, U64: n}, nil
		}
	}
	f, err := ParseF64(lit)
	if err != nil {
		return Content{}, d.Fail(err.Error())
	}
	return Content{Kind: KindNumber, F: f}, nil
}

// JSON writes the content back as JSON text.
func (c *Content) JSON() []byte {
	var b bytes.Buffer
	c.write(&b)
	return b.Bytes()
}

func (c *Content) write(b *bytes.Buffer) {
	switch c.Kind {
	case KindNull:
		b.WriteString("null")
	case KindBool:
		b.WriteString(strconv.FormatBool(c.Bool))
	case KindNumber:
		switch {
		case c.Int && c.Neg:
			b.WriteString(strconv.FormatInt(c.I64, 10))
		case c.Int:
			b.WriteString(strconv.FormatUint(c.U64, 10))
		default:
			b.WriteString(FormatF64(c.F))
		}
	case KindString:
		writeString(b, c.Str)
	case KindArray:
		b.WriteByte('[')
		for i := range c.Seq {
			if i > 0 {
				b.WriteByte(',')
			}
			c.Seq[i].write(b)
		}
		b.WriteByte(']')
	case KindObject:
		b.WriteByte('{')
		for i, k := range c.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			c.Vals[i].write(b)
		}
		b.WriteByte('}')
	}
}

// taggedContent splits an internally tagged enum's buffered value into its variant and fields.
// From JSON text (fromJSON) serde_json reads the tag as a string only; from Content, a variant
// index works too.
func taggedContent(c *Content, tag string, variants []string, fromJSON bool) (int, Decoder, error) {
	var tagValue *Content
	var rest Content
	switch c.Kind {
	case KindObject:
		rest = Content{Kind: KindObject}
		for i, k := range c.Keys {
			if k == tag {
				if tagValue != nil {
					return 0, nil, contentFail("duplicate field `" + tag + "`")
				}
				tagValue = &c.Vals[i]
				continue
			}
			rest.Keys = append(rest.Keys, k)
			rest.Vals = append(rest.Vals, c.Vals[i])
		}
	case KindArray:
		if len(c.Seq) > 0 {
			tagValue = &c.Seq[0]
		}
		rest = Content{Kind: KindArray, Seq: c.Seq[min(1, len(c.Seq)):]}
	default:
		return 0, nil, contentFail("invalid type, expected an internally tagged enum")
	}
	if tagValue == nil {
		return 0, nil, contentFail("missing field `" + tag + "`")
	}
	if fromJSON && tagValue.Kind != KindString {
		return 0, nil, contentFail("invalid type, expected variant identifier")
	}
	i, err := variantIndex(&contentDecoder{cur: tagValue}, variants)
	if err != nil {
		return 0, nil, err
	}
	return i, &contentDecoder{cur: &rest}, nil
}

func contentFail(msg string) error { return &Error{msg, -1} }

// DecodeContent decodes buffered content into v as serde's ContentDeserializer does.
func DecodeContent(c *Content, v any) error {
	return (&contentDecoder{cur: c}).Decode(v)
}

// contentDecoder reads buffered Content with serde's ContentDeserializer rules: no recursion
// limit, null is None for an Option, floats come from f64 (an f32 is the f64 rounded), integers
// are range-checked, a unit variant's value may also be {}, and a map's keys are strings only.
type contentDecoder struct {
	cur *Content
	// fromValue: the content is a serde_json::Value (serde_json::from_value), whose deserializer
	// differs from Content's: a unit is null only, numeric map keys are read from the key's
	// text, and identifiers are names only.
	fromValue bool
}

func (d *contentDecoder) Fail(msg string) error { return contentFail(msg) }

func (d *contentDecoder) Peek() Kind {
	if d.cur == nil {
		return KindInvalid
	}
	return d.cur.Kind
}

func (d *contentDecoder) typeError(expected string) error {
	names := map[Kind]string{KindObject: "map", KindArray: "sequence", KindString: "string", KindBool: "boolean", KindNull: "unit value", KindNumber: "number"}
	return d.Fail("invalid type: " + names[d.Peek()] + ", expected " + expected)
}

func (d *contentDecoder) Null() (bool, error) { return d.Peek() == KindNull, nil }

func (d *contentDecoder) Bool() (bool, error) {
	if d.Peek() != KindBool {
		return false, d.typeError("a boolean")
	}
	return d.cur.Bool, nil
}

func (d *contentDecoder) String() (string, error) {
	if d.Peek() != KindString {
		return "", d.typeError("a string")
	}
	return d.cur.Str, nil
}

func (d *contentDecoder) Float(bits int) (float64, error) {
	if d.Peek() != KindNumber {
		return 0, d.typeError("a float")
	}
	c := d.cur
	var f float64
	switch {
	case c.Int && c.Neg:
		if bits == 32 {
			return float64(float32(c.I64)), nil
		}
		f = float64(c.I64)
	case c.Int:
		if bits == 32 {
			return float64(float32(c.U64)), nil
		}
		f = float64(c.U64)
	default:
		f = c.F
	}
	if bits == 32 {
		// Rust's `f as f32`.
		return float64(float32(f)), nil
	}
	return f, nil
}

func (d *contentDecoder) Int(bits int) (int64, error) {
	c := d.cur
	if d.Peek() != KindNumber || !c.Int {
		return 0, d.typeError("an integer")
	}
	lo, hi := int64(-1)<<(bits-1), int64(1)<<(bits-1)-1
	switch {
	case c.Neg && c.I64 >= lo:
		return c.I64, nil
	case !c.Neg && c.U64 <= uint64(hi):
		return int64(c.U64), nil
	}
	return 0, d.Fail("invalid value: integer out of range")
}

func (d *contentDecoder) Uint(bits int) (uint64, error) {
	c := d.cur
	if d.Peek() != KindNumber || !c.Int {
		return 0, d.typeError("an unsigned integer")
	}
	if c.Neg || bits < 64 && c.U64 > uint64(1)<<bits-1 {
		return 0, d.Fail("invalid value: integer out of range")
	}
	return c.U64, nil
}

func (d *contentDecoder) Object(each func(k Key) error) error {
	if d.Peek() != KindObject {
		return d.typeError("a map")
	}
	c := d.cur
	defer func() { d.cur = c }()
	for i, k := range c.Keys {
		d.cur = &c.Vals[i]
		if err := each(Key{Name: k, raw: k, fromContent: !d.fromValue}); err != nil {
			return err
		}
	}
	return nil
}

func (d *contentDecoder) Array(each func(i int) error) error {
	if d.Peek() != KindArray {
		return d.typeError("a sequence")
	}
	c := d.cur
	defer func() { d.cur = c }()
	for i := range c.Seq {
		d.cur = &c.Seq[i]
		if err := each(i); err != nil {
			return err
		}
	}
	return nil
}

func (d *contentDecoder) Skip() error { return nil }

func (d *contentDecoder) Raw() ([]byte, error) {
	if d.cur == nil {
		return nil, d.Fail("no value")
	}
	return d.cur.JSON(), nil
}

func (d *contentDecoder) Content() (Content, error) {
	if d.cur == nil {
		return Content{}, d.Fail("no value")
	}
	return *d.cur, nil
}

func (d *contentDecoder) Decode(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("serdejson: Decode needs a non-nil pointer")
	}
	return decodeValue(d, rv.Elem())
}

// UnitVariant reads "name", or {"name": null} or {"name": {}} (serde's Content takes an empty map
// as a unit).
func (d *contentDecoder) UnitVariant(names []string) (int, error) {
	switch d.Peek() {
	case KindString:
		return variantIndex(d, names)
	case KindObject:
		c := d.cur
		if len(c.Keys) != 1 {
			return 0, d.Fail("invalid value: map, expected map with a single key")
		}
		i, err := variantIndex(&contentDecoder{cur: &Content{Kind: KindString, Str: c.Keys[0]}}, names)
		if err != nil {
			return 0, err
		}
		v := c.Vals[0]
		if v.Kind == KindNull || !d.fromValue && v.Kind == KindObject && len(v.Keys) == 0 {
			return i, nil
		}
		return 0, d.Fail("invalid type, expected unit variant")
	}
	return 0, d.typeError("string or map")
}

func (d *contentDecoder) Tagged(tag string, variants []string) (int, Decoder, error) {
	if d.cur == nil {
		return 0, nil, d.Fail("no value")
	}
	return taggedContent(d.cur, tag, variants, d.fromValue)
}

// Value reads a serde_json::Value from Content.
func (d *contentDecoder) Value() (Value, error) {
	if d.cur == nil {
		return Value{}, d.Fail("no value")
	}
	return contentValue(d.cur), nil
}

func contentValue(c *Content) Value {
	switch c.Kind {
	case KindBool:
		return Value{Kind: KindBool, Bool: c.Bool}
	case KindNumber:
		switch {
		case c.Int && c.Neg:
			return Value{Kind: KindNumber, Num: strconv.FormatInt(c.I64, 10), IsInt: true}
		case c.Int:
			return Value{Kind: KindNumber, Num: strconv.FormatUint(c.U64, 10), IsInt: true}
		}
		if math.IsNaN(c.F) || math.IsInf(c.F, 0) {
			return Value{Kind: KindNull}
		}
		return Value{Kind: KindNumber, Num: FormatF64(c.F)}
	case KindString:
		return Value{Kind: KindString, Str: c.Str}
	case KindArray:
		v := Value{Kind: KindArray}
		for i := range c.Seq {
			v.Arr = append(v.Arr, contentValue(&c.Seq[i]))
		}
		return v
	case KindObject:
		v := Value{Kind: KindObject, Fields: map[string]Value{}}
		for i, k := range c.Keys {
			if _, dup := v.Fields[k]; !dup {
				v.Keys = append(v.Keys, k)
			}
			v.Fields[k] = contentValue(&c.Vals[i])
		}
		sortStrings(v.Keys)
		return v
	}
	return Value{Kind: KindNull}
}
