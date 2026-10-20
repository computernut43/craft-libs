package serdejson

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// Kind is the kind of the next value.
type Kind byte

// Value kinds.
const (
	KindInvalid Kind = iota // end of input or not a value
	KindNull
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Decoder is where a value is read from: JSON text (Unmarshal), or serde's buffered Content
// (inside an internally tagged enum, which serde reads into Content first and then decodes with
// slightly different rules). Types with a hand-written Rust Deserialize implement SerdeDecoder
// and read their value through it.
type Decoder interface {
	// Peek is the kind of the next value.
	Peek() Kind
	// Null consumes a null and reports it; anything else is left for the caller.
	Null() (bool, error)
	Bool() (bool, error)
	// String reads a string (checked as serde_json checks the strings it decodes).
	String() (string, error)
	// Float reads an f32 (bits 32) or f64 as serde reads one from this source.
	Float(bits int) (float64, error)
	// Int and Uint read an integer of the given size (a float is an error).
	Int(bits int) (int64, error)
	Uint(bits int) (uint64, error)
	// Object reads a map, calling each with every key; each must read the value (Decode, Skip,
	// …).
	Object(each func(k Key) error) error
	// Array reads a sequence, calling each for every element; each must read it.
	Array(each func(i int) error) error
	// Skip reads a value without decoding it (an unknown field).
	Skip() error
	// Raw reads a value and returns it as JSON text.
	Raw() ([]byte, error)
	// Value reads a value as serde_json::Value.
	Value() (Value, error)
	// Content reads a value as serde buffers one (serde's Content).
	Content() (Content, error)
	// UnitVariant reads a unit variant of an externally tagged enum and returns its index.
	UnitVariant(names []string) (int, error)
	// Tagged reads an internally tagged enum (#[serde(tag = "…")]): it returns the variant's
	// index and a Decoder for the rest of its fields.
	Tagged(tag string, variants []string) (int, Decoder, error)
	// Decode reads the next value into v (a non-nil pointer).
	Decode(v any) error
	// Fail is an error at the current position.
	Fail(msg string) error
}

// SerdeDecoder is a type with a hand-written Rust Deserialize (implemented on the pointer).
type SerdeDecoder interface{ DecodeSerde(d Decoder) error }

// Defaulter marks a struct whose missing fields take their defaults, as #[serde(default)] on a
// Rust struct does. Without it, a field must be present unless it is a pointer (an Option) or
// tagged `serde:"default"`.
type Defaulter interface{ SerdeDefault() }

// DefaultSetter is a type whose Rust Default isn't Go's zero value. Decoding a Defaulter struct
// starts from SetDefaults; a missing field tagged `serde:"default"` gets its type's
// SetDefaults.
type DefaultSetter interface{ SetDefaults() }

var (
	defaulterType    = reflect.TypeFor[Defaulter]()
	serdeDecoderType = reflect.TypeFor[SerdeDecoder]()
)

// Error is input serde would refuse.
type Error struct {
	Msg    string
	Offset int // byte offset in JSON input; -1 inside buffered Content
}

func (e *Error) Error() string {
	if e.Offset < 0 {
		return "serdejson: " + e.Msg
	}
	return fmt.Sprintf("serdejson: %s at byte %d", e.Msg, e.Offset)
}

// Key is an object key: its decoded text, and how a numeric map key reads from it (serde_json
// reads a numeric key from the key's text as written; serde's Content can't give one at all).
type Key struct {
	Name string
	raw  string
	// fromContent: the key is buffered Content (a string), which serde won't read as a number.
	fromContent bool
}

// numeric reads the key as an integer map key of type kt.
func (k Key) numeric(kt reflect.Type) (reflect.Value, error) {
	if k.fromContent {
		return reflect.Value{}, errors.New("invalid type: string, expected " + kt.String())
	}
	raw := k.raw
	if raw == "" || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return reflect.Value{}, errors.New("expected numeric key")
	}
	p := &jsonDecoder{b: []byte(raw)}
	if p.scanNumber() != nil || p.pos != len(raw) {
		return reflect.Value{}, errors.New("invalid numeric key")
	}
	v := reflect.New(kt).Elem()
	if isUnsigned(kt.Kind()) {
		n, ok := uintLiteral(raw, kt.Bits())
		if !ok {
			return reflect.Value{}, errors.New("invalid numeric key")
		}
		v.SetUint(n)
	} else {
		n, ok := intLiteral(raw, kt.Bits())
		if !ok {
			return reflect.Value{}, errors.New("invalid numeric key")
		}
		v.SetInt(n)
	}
	return v, nil
}

func isUnsigned(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// intLiteral is a number literal serde_json reads as an integer that fits bits: no fraction or
// exponent, and not "-0" (which it reads as the float -0.0).
func intLiteral(lit string, bits int) (int64, bool) {
	if !isInteger(lit) || lit == "-0" {
		return 0, false
	}
	n, err := strconv.ParseInt(lit, 10, bits)
	return n, err == nil
}

func uintLiteral(lit string, bits int) (uint64, bool) {
	if !isInteger(lit) || len(lit) > 0 && lit[0] == '-' {
		return 0, false
	}
	n, err := strconv.ParseUint(lit, 10, bits)
	return n, err == nil
}

// variantIndex finds a variant by name, or by index (serde's variant identifiers also read
// integers).
func variantIndex(d Decoder, variants []string) (int, error) {
	switch d.Peek() {
	case KindString:
		s, err := d.String()
		if err != nil {
			return 0, err
		}
		for i, v := range variants {
			if v == s {
				return i, nil
			}
		}
		return 0, d.Fail("unknown variant `" + s + "`")
	case KindNumber:
		n, err := d.Uint(64)
		if err != nil {
			return 0, err
		}
		if n >= uint64(len(variants)) {
			return 0, d.Fail(fmt.Sprintf("invalid value: integer `%d`, expected variant index 0 <= i < %d", n, len(variants)))
		}
		return int(n), nil
	}
	return 0, d.Fail("invalid type, expected variant identifier")
}
