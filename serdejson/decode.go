package serdejson

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// RecursionLimit is how deeply serde_json lets the arrays and objects it decodes nest: it fails
// on the 128th. Values it skips (unknown fields) don't count and may nest without limit.
const RecursionLimit = 127

// Defaulter marks a struct whose missing fields take their defaults, as #[serde(default)] on a
// Rust struct does. Without it, a field must be present unless it is a pointer (an Option) or
// tagged `serde:"default"`.
type Defaulter interface{ SerdeDefault() }

// DefaultSetter is a type whose Rust Default isn't Go's zero value. Decoding a Defaulter struct
// starts from SetDefaults; a missing field tagged `serde:"default"` gets its type's
// SetDefaults.
type DefaultSetter interface{ SetDefaults() }

// SerdeDecoder is a type with its own Deserialize: it reads its value from d (implemented on the
// pointer).
type SerdeDecoder interface{ DecodeSerde(d *Decoder) error }

var (
	defaulterType    = reflect.TypeFor[Defaulter]()
	serdeDecoderType = reflect.TypeFor[SerdeDecoder]()
)

// Error is JSON serde_json would refuse, at a byte offset.
type Error struct {
	Msg    string
	Offset int
}

func (e *Error) Error() string { return fmt.Sprintf("serdejson: %s at byte %d", e.Msg, e.Offset) }

// Decoder reads one JSON value with serde_json's rules, driven by the Go types it decodes into.
// Types that implement SerdeDecoder use its methods to read their own value.
type Decoder struct {
	b     []byte
	pos   int
	depth int // decoded containers open
}

// Unmarshal decodes data into v (a non-nil pointer) as serde_json::from_slice decodes the Rust
// type v mirrors.
func Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("serdejson: Unmarshal needs a non-nil pointer")
	}
	d := &Decoder{b: data}
	if err := d.value(rv.Elem()); err != nil {
		return err
	}
	d.skipSpace()
	if d.pos != len(d.b) {
		return d.fail("trailing characters")
	}
	return nil
}

func (d *Decoder) fail(msg string) error { return &Error{msg, d.pos} }

func (d *Decoder) skipSpace() {
	for d.pos < len(d.b) {
		switch d.b[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return
		}
	}
}

// Peek is the first byte of the next value ('{', '[', '"', 't', 'f', 'n', a digit or '-'), or 0
// at the end of the input.
func (d *Decoder) Peek() byte {
	d.skipSpace()
	if d.pos >= len(d.b) {
		return 0
	}
	return d.b[d.pos]
}

// Null consumes a null and reports it; anything else is left for the caller.
func (d *Decoder) Null() (bool, error) {
	if d.Peek() != 'n' {
		return false, nil
	}
	return true, d.ident("null")
}

func (d *Decoder) ident(word string) error {
	if !strings.HasPrefix(string(d.b[d.pos:min(len(d.b), d.pos+len(word))]), word) {
		return d.fail("expected ident")
	}
	d.pos += len(word)
	return nil
}

// enter opens a decoded array or object.
func (d *Decoder) enter() error {
	d.depth++
	if d.depth > RecursionLimit {
		return d.fail("recursion limit exceeded")
	}
	return nil
}

// Object reads an object, calling each(key) for every key; each must consume the value (Decode
// or Skip). Keys are decoded and checked as serde_json checks the strings it reads.
func (d *Decoder) Object(each func(key string) error) error {
	return d.object(func(key, _ string) error { return each(key) })
}

// object reads an object, handing each callback the decoded key and its text as written
// (between the quotes; numeric keys are read from it).
func (d *Decoder) object(each func(key, raw string) error) error {
	if d.Peek() != '{' {
		return d.typeError("a map")
	}
	if err := d.enter(); err != nil {
		return err
	}
	d.pos++
	if d.Peek() == '}' {
		d.pos++
		d.depth--
		return nil
	}
	for {
		if d.Peek() != '"' {
			return d.fail("key must be a string")
		}
		start := d.pos
		key, err := d.String()
		if err != nil {
			return err
		}
		raw := string(d.b[start+1 : d.pos-1])
		if d.Peek() != ':' {
			return d.fail("expected `:`")
		}
		d.pos++
		if err := each(key, raw); err != nil {
			return err
		}
		switch d.Peek() {
		case ',':
			d.pos++
			if d.Peek() == '}' {
				return d.fail("trailing comma")
			}
		case '}':
			d.pos++
			d.depth--
			return nil
		default:
			return d.fail("expected `,` or `}`")
		}
	}
}

// Array reads an array, calling each(i) for every element; each must consume it.
func (d *Decoder) Array(each func(i int) error) error {
	if d.Peek() != '[' {
		return d.typeError("a sequence")
	}
	if err := d.enter(); err != nil {
		return err
	}
	d.pos++
	if d.Peek() == ']' {
		d.pos++
		d.depth--
		return nil
	}
	for i := 0; ; i++ {
		if err := each(i); err != nil {
			return err
		}
		switch d.Peek() {
		case ',':
			d.pos++
			if d.Peek() == ']' {
				return d.fail("trailing comma")
			}
		case ']':
			d.pos++
			d.depth--
			return nil
		default:
			return d.fail("expected `,` or `]`")
		}
	}
}

func (d *Decoder) typeError(expected string) error {
	return d.fail("invalid type: " + d.kindName() + ", expected " + expected)
}

func (d *Decoder) kindName() string {
	switch c := d.Peek(); {
	case c == '{':
		return "map"
	case c == '[':
		return "sequence"
	case c == '"':
		return "string"
	case c == 't' || c == 'f':
		return "boolean"
	case c == 'n':
		return "null"
	case c == '-' || c >= '0' && c <= '9':
		return "number"
	}
	return "invalid value"
}

// String reads a string value: no raw control characters, valid escapes, surrogates paired, and
// valid UTF-8 (serde_json checks the strings it decodes; it doesn't check those it skips).
func (d *Decoder) String() (string, error) {
	if d.Peek() != '"' {
		return "", d.typeError("a string")
	}
	d.pos++
	var out []byte
	start := d.pos
	for {
		if d.pos >= len(d.b) {
			return "", d.fail("EOF while parsing a string")
		}
		c := d.b[d.pos]
		switch {
		case c == '"':
			out = append(out, d.b[start:d.pos]...)
			d.pos++
			if !utf8.Valid(out) {
				return "", d.fail("invalid unicode code point")
			}
			return string(out), nil
		case c < 0x20:
			return "", d.fail("control character (\\u0000-\\u001F) found while parsing a string")
		case c == '\\':
			out = append(out, d.b[start:d.pos]...)
			d.pos++
			if d.pos >= len(d.b) {
				return "", d.fail("EOF while parsing a string")
			}
			e := d.b[d.pos]
			d.pos++
			switch e {
			case '"', '\\', '/':
				out = append(out, e)
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, err := d.hex4()
				if err != nil {
					return "", err
				}
				switch {
				case r >= 0xDC00 && r <= 0xDFFF:
					return "", d.fail("lone leading surrogate in hex escape")
				case r >= 0xD800 && r <= 0xDBFF:
					if d.pos+2 > len(d.b) || d.b[d.pos] != '\\' || d.b[d.pos+1] != 'u' {
						return "", d.fail("unexpected end of hex escape")
					}
					d.pos += 2
					r2, err := d.hex4()
					if err != nil {
						return "", err
					}
					if r2 < 0xDC00 || r2 > 0xDFFF {
						return "", d.fail("lone leading surrogate in hex escape")
					}
					r = utf16.DecodeRune(r, r2)
				}
				out = utf8.AppendRune(out, r)
			default:
				return "", d.fail("invalid escape")
			}
			start = d.pos
		default:
			d.pos++
		}
	}
}

func (d *Decoder) hex4() (rune, error) {
	if d.pos+4 > len(d.b) {
		return 0, d.fail("EOF while parsing a string")
	}
	var v rune
	for _, c := range d.b[d.pos : d.pos+4] {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, d.fail("invalid escape")
		}
	}
	d.pos += 4
	return v, nil
}

// Number reads a number's literal (RFC 8259 grammar: no leading zeros, '+' or bare '.').
func (d *Decoder) Number() (string, error) {
	c := d.Peek()
	if c != '-' && (c < '0' || c > '9') {
		return "", d.typeError("a number")
	}
	start := d.pos
	if err := d.scanNumber(); err != nil {
		return "", err
	}
	return string(d.b[start:d.pos]), nil
}

func (d *Decoder) scanNumber() error {
	digits := func() int {
		n := 0
		for d.pos < len(d.b) && d.b[d.pos] >= '0' && d.b[d.pos] <= '9' {
			d.pos++
			n++
		}
		return n
	}
	if d.b[d.pos] == '-' {
		d.pos++
	}
	if d.pos >= len(d.b) {
		return d.fail("EOF while parsing a value")
	}
	if d.b[d.pos] == '0' {
		d.pos++
		if d.pos < len(d.b) && d.b[d.pos] >= '0' && d.b[d.pos] <= '9' {
			return d.fail("invalid number")
		}
	} else if digits() == 0 {
		return d.fail("invalid number")
	}
	if d.pos < len(d.b) && d.b[d.pos] == '.' {
		d.pos++
		if digits() == 0 {
			return d.fail("invalid number")
		}
	}
	if d.pos < len(d.b) && (d.b[d.pos] == 'e' || d.b[d.pos] == 'E') {
		d.pos++
		if d.pos < len(d.b) && (d.b[d.pos] == '+' || d.b[d.pos] == '-') {
			d.pos++
		}
		if digits() == 0 {
			return d.fail("invalid number")
		}
	}
	return nil
}

// Skip consumes the next value without decoding it, as serde_json ignores an unknown field:
// any depth (an explicit stack, not recursion), strings checked only for control characters and
// escape syntax.
func (d *Decoder) Skip() error {
	var stack []byte
	for {
		switch c := d.Peek(); {
		case c == 0:
			return d.fail("EOF while parsing a value")
		case c == '{' || c == '[':
			d.pos++
			closing := byte('}')
			if c == '[' {
				closing = ']'
			}
			if d.Peek() != closing {
				stack = append(stack, closing)
				if c == '{' {
					if err := d.skipKey(); err != nil {
						return err
					}
				}
				continue
			}
			d.pos++
		case c == '"':
			if err := d.skipString(); err != nil {
				return err
			}
		case c == 't':
			if err := d.ident("true"); err != nil {
				return err
			}
		case c == 'f':
			if err := d.ident("false"); err != nil {
				return err
			}
		case c == 'n':
			if err := d.ident("null"); err != nil {
				return err
			}
		case c == '-' || c >= '0' && c <= '9':
			if err := d.scanNumber(); err != nil {
				return err
			}
		default:
			return d.fail("expected value")
		}
		// After a value: close what ends here, or move to the next element.
		for next := false; !next; {
			if len(stack) == 0 {
				return nil
			}
			closing := stack[len(stack)-1]
			switch d.Peek() {
			case ',':
				d.pos++
				if closing == '}' {
					if err := d.skipKey(); err != nil {
						return err
					}
				}
				next = true
			case closing:
				d.pos++
				stack = stack[:len(stack)-1]
			default:
				return d.fail("expected `,` or `" + string(closing) + "`")
			}
		}
	}
}

func (d *Decoder) skipKey() error {
	if d.Peek() != '"' {
		return d.fail("key must be a string")
	}
	if err := d.skipString(); err != nil {
		return err
	}
	if d.Peek() != ':' {
		return d.fail("expected `:`")
	}
	d.pos++
	return nil
}

// skipString consumes a string as serde_json's ignore_str: control characters and malformed
// escapes are errors; \u escapes need four hex digits but needn't pair up; bytes aren't checked.
func (d *Decoder) skipString() error {
	d.pos++
	for {
		if d.pos >= len(d.b) {
			return d.fail("EOF while parsing a string")
		}
		c := d.b[d.pos]
		d.pos++
		switch {
		case c == '"':
			return nil
		case c < 0x20:
			return d.fail("control character (\\u0000-\\u001F) found while parsing a string")
		case c == '\\':
			if d.pos >= len(d.b) {
				return d.fail("EOF while parsing a string")
			}
			e := d.b[d.pos]
			d.pos++
			switch e {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if _, err := d.hex4(); err != nil {
					return err
				}
			default:
				return d.fail("invalid escape")
			}
		}
	}
}

// Raw consumes the next value and returns its text (for fields whose type isn't modelled yet:
// it is checked only as a skipped value would be).
func (d *Decoder) Raw() ([]byte, error) {
	d.skipSpace()
	start := d.pos
	if err := d.Skip(); err != nil {
		return nil, err
	}
	return d.b[start:d.pos], nil
}

// Decode reads the next value into v (a non-nil pointer), counting nesting with the values
// around it.
func (d *Decoder) Decode(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("serdejson: Decode needs a non-nil pointer")
	}
	return d.value(rv.Elem())
}

// field is a struct field as serde sees it.
type field struct {
	name     string
	index    []int
	typ      reflect.Type
	optional bool // may be missing (an Option, or a default)
	tagged   bool // `serde:"default"`
}

type structInfo struct {
	fields     []field // declaration order
	byName     map[string]int
	allDefault bool
}

var structCache sync.Map // reflect.Type → *structInfo

func infoOf(t reflect.Type) *structInfo {
	if v, ok := structCache.Load(t); ok {
		if si, ok := v.(*structInfo); ok {
			return si
		}
	}
	si := &structInfo{byName: map[string]int{}}
	si.allDefault = t.Implements(defaulterType) || reflect.PointerTo(t).Implements(defaulterType)
	var collect func(reflect.Type, []int)
	collect = func(t reflect.Type, at []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(append([]int(nil), at...), i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				collect(f.Type, index)
				continue
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			tagged := strings.Contains(f.Tag.Get("serde"), "default")
			si.byName[name] = len(si.fields)
			si.fields = append(si.fields, field{
				name: name, index: index, typ: f.Type, tagged: tagged,
				optional: si.allDefault || tagged || f.Type.Kind() == reflect.Pointer,
			})
		}
	}
	collect(t, nil)
	structCache.Store(t, si)
	return si
}

// value decodes the next value into v (settable).
func (d *Decoder) value(v reflect.Value) error {
	t := v.Type()
	if reflect.PointerTo(t).Implements(serdeDecoderType) {
		dec, ok := v.Addr().Interface().(SerdeDecoder)
		if !ok {
			return errors.New("serdejson: " + t.String() + " can't decode itself")
		}
		return dec.DecodeSerde(d)
	}
	switch t.Kind() {
	case reflect.Pointer:
		if null, err := d.Null(); null || err != nil {
			v.SetZero()
			return err
		}
		p := reflect.New(t.Elem())
		if err := d.value(p.Elem()); err != nil {
			return err
		}
		v.Set(p)
		return nil
	case reflect.Struct:
		return d.structValue(v)
	case reflect.Map:
		return d.mapValue(v)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 0, 0)
		err := d.Array(func(int) error {
			e := reflect.New(t.Elem()).Elem()
			if err := d.value(e); err != nil {
				return err
			}
			s = reflect.Append(s, e)
			return nil
		})
		if err != nil {
			return err
		}
		v.Set(s)
		return nil
	case reflect.Array:
		n := 0
		err := d.Array(func(i int) error {
			if i >= t.Len() {
				return d.fail(fmt.Sprintf("trailing characters (expected %d elements)", t.Len()))
			}
			n = i + 1
			return d.value(v.Index(i))
		})
		if err != nil {
			return err
		}
		if n != t.Len() {
			return d.fail(fmt.Sprintf("invalid length %d, expected an array of length %d", n, t.Len()))
		}
		return nil
	case reflect.String:
		s, err := d.String()
		if err != nil {
			return err
		}
		v.SetString(s)
		return nil
	case reflect.Bool:
		switch d.Peek() {
		case 't':
			v.SetBool(true)
			return d.ident("true")
		case 'f':
			v.SetBool(false)
			return d.ident("false")
		}
		return d.typeError("a boolean")
	case reflect.Float32, reflect.Float64:
		lit, err := d.Number()
		if err != nil {
			return err
		}
		var f float64
		if t.Kind() == reflect.Float32 {
			var f32 float32
			f32, err = ParseF32(lit)
			f = float64(f32)
		} else {
			f, err = ParseF64(lit)
		}
		if err != nil {
			return d.fail(err.Error())
		}
		v.SetFloat(f)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		lit, err := d.Number()
		if err != nil {
			return err
		}
		n, ok := intLiteral(lit, t.Bits())
		if !ok {
			return d.fail("invalid value for " + t.String())
		}
		v.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		lit, err := d.Number()
		if err != nil {
			return err
		}
		n, ok := uintLiteral(lit, t.Bits())
		if !ok {
			return d.fail("invalid value for " + t.String())
		}
		v.SetUint(n)
		return nil
	}
	return errors.New("serdejson: can't decode into " + t.String())
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
	if !isInteger(lit) || strings.HasPrefix(lit, "-") {
		return 0, false
	}
	n, err := strconv.ParseUint(lit, 10, bits)
	return n, err == nil
}

func (d *Decoder) structValue(v reflect.Value) error {
	t := v.Type()
	si := infoOf(t)
	fresh := reflect.New(t).Elem()
	if si.allDefault {
		setDefault(fresh)
	}
	seen := make([]bool, len(si.fields))
	switch d.Peek() {
	case '{':
		err := d.Object(func(key string) error {
			fi, ok := si.byName[key]
			if !ok {
				return d.Skip()
			}
			if seen[fi] {
				return d.fail("duplicate field `" + key + "`")
			}
			seen[fi] = true
			return d.value(fresh.FieldByIndex(si.fields[fi].index))
		})
		if err != nil {
			return err
		}
		for fi, f := range si.fields {
			if seen[fi] {
				continue
			}
			if !f.optional {
				return d.fail("missing field `" + f.name + "`")
			}
			if f.tagged && !si.allDefault {
				setDefault(fresh.FieldByIndex(f.index))
			}
		}
	case '[':
		// serde takes a struct as the sequence of its fields in order.
		n := 0
		err := d.Array(func(i int) error {
			if i >= len(si.fields) {
				return d.fail("trailing characters in sequence for struct " + t.Name())
			}
			n = i + 1
			return d.value(fresh.FieldByIndex(si.fields[i].index))
		})
		if err != nil {
			return err
		}
		for i := n; i < len(si.fields); i++ {
			f := si.fields[i]
			if !si.allDefault && !f.tagged {
				return d.fail(fmt.Sprintf("invalid length %d, expected struct %s with %d elements", n, t.Name(), len(si.fields)))
			}
			if f.tagged && !si.allDefault {
				setDefault(fresh.FieldByIndex(f.index))
			}
		}
	default:
		return d.typeError("struct " + t.Name())
	}
	v.Set(fresh)
	return nil
}

func setDefault(v reflect.Value) {
	if ds, ok := v.Addr().Interface().(DefaultSetter); ok {
		ds.SetDefaults()
	}
}

func (d *Decoder) mapValue(v reflect.Value) error {
	t := v.Type()
	m := reflect.MakeMap(t)
	kt := t.Key()
	numeric := false
	switch kt.Kind() {
	case reflect.String:
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		numeric = true
	default:
		return errors.New("serdejson: can't decode map keys of " + kt.String())
	}
	err := d.object(func(key, raw string) error {
		var k reflect.Value
		if numeric {
			var err error
			if k, err = numericKey(raw, kt); err != nil {
				return d.fail(err.Error())
			}
		} else {
			k = reflect.New(kt).Elem()
			k.SetString(key)
		}
		e := reflect.New(t.Elem()).Elem()
		if err := d.value(e); err != nil {
			return err
		}
		m.SetMapIndex(k, e)
		return nil
	})
	if err != nil {
		return err
	}
	v.Set(m)
	return nil
}

// numericKey reads a key's text as serde_json reads an integer map key: the whole text must be
// a JSON integer for the key type (no escapes, spaces, leading zeros, fraction or exponent).
func numericKey(raw string, kt reflect.Type) (reflect.Value, error) {
	if raw == "" || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return reflect.Value{}, errors.New("expected numeric key")
	}
	p := &Decoder{b: []byte(raw)}
	if p.scanNumber() != nil || p.pos != len(raw) {
		return reflect.Value{}, errors.New("invalid numeric key")
	}
	k := reflect.New(kt).Elem()
	switch kt.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, ok := uintLiteral(raw, kt.Bits())
		if !ok {
			return reflect.Value{}, errors.New("invalid numeric key")
		}
		k.SetUint(n)
	default:
		n, ok := intLiteral(raw, kt.Bits())
		if !ok {
			return reflect.Value{}, errors.New("invalid numeric key")
		}
		k.SetInt(n)
	}
	return k, nil
}
