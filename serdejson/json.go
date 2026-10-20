package serdejson

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// RecursionLimit is how deeply serde_json lets the arrays and objects it decodes nest: it fails
// on the 128th. Values it skips (unknown fields) don't count and may nest without limit.
const RecursionLimit = 127

// Unmarshal decodes data into v (a non-nil pointer) as serde_json::from_slice decodes the Rust
// type v mirrors.
func Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("serdejson: Unmarshal needs a non-nil pointer")
	}
	d := &jsonDecoder{b: data}
	if err := decodeValue(d, rv.Elem()); err != nil {
		return err
	}
	d.skipSpace()
	if d.pos != len(d.b) {
		return d.Fail("trailing characters")
	}
	return nil
}

// jsonDecoder reads JSON text with serde_json's rules.
type jsonDecoder struct {
	b     []byte
	pos   int
	depth int // decoded containers open
}

func (d *jsonDecoder) Fail(msg string) error { return &Error{msg, d.pos} }

func (d *jsonDecoder) skipSpace() {
	for d.pos < len(d.b) {
		switch d.b[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return
		}
	}
}

func (d *jsonDecoder) peekByte() byte {
	d.skipSpace()
	if d.pos >= len(d.b) {
		return 0
	}
	return d.b[d.pos]
}

func (d *jsonDecoder) Peek() Kind {
	switch c := d.peekByte(); {
	case c == '{':
		return KindObject
	case c == '[':
		return KindArray
	case c == '"':
		return KindString
	case c == 't' || c == 'f':
		return KindBool
	case c == 'n':
		return KindNull
	case c == '-' || c >= '0' && c <= '9':
		return KindNumber
	}
	return KindInvalid
}

func (d *jsonDecoder) ident(word string) error {
	if !strings.HasPrefix(string(d.b[d.pos:min(len(d.b), d.pos+len(word))]), word) {
		return d.Fail("expected ident")
	}
	d.pos += len(word)
	return nil
}

func (d *jsonDecoder) Null() (bool, error) {
	if d.peekByte() != 'n' {
		return false, nil
	}
	return true, d.ident("null")
}

func (d *jsonDecoder) Bool() (bool, error) {
	switch d.peekByte() {
	case 't':
		return true, d.ident("true")
	case 'f':
		return false, d.ident("false")
	}
	return false, d.typeError("a boolean")
}

func (d *jsonDecoder) typeError(expected string) error {
	names := map[Kind]string{KindObject: "map", KindArray: "sequence", KindString: "string", KindBool: "boolean", KindNull: "null", KindNumber: "number"}
	got, ok := names[d.Peek()]
	if !ok {
		got = "invalid value"
	}
	return d.Fail("invalid type: " + got + ", expected " + expected)
}

// enter opens a decoded array or object.
func (d *jsonDecoder) enter() error {
	d.depth++
	if d.depth > RecursionLimit {
		return d.Fail("recursion limit exceeded")
	}
	return nil
}

func (d *jsonDecoder) Object(each func(k Key) error) error {
	if d.peekByte() != '{' {
		return d.typeError("a map")
	}
	if err := d.enter(); err != nil {
		return err
	}
	d.pos++
	if d.peekByte() == '}' {
		d.pos++
		d.depth--
		return nil
	}
	for {
		if d.peekByte() != '"' {
			return d.Fail("key must be a string")
		}
		start := d.pos
		name, err := d.String()
		if err != nil {
			return err
		}
		raw := string(d.b[start+1 : d.pos-1])
		if d.peekByte() != ':' {
			return d.Fail("expected `:`")
		}
		d.pos++
		if err := each(Key{Name: name, raw: raw}); err != nil {
			return err
		}
		switch d.peekByte() {
		case ',':
			d.pos++
			if d.peekByte() == '}' {
				return d.Fail("trailing comma")
			}
		case '}':
			d.pos++
			d.depth--
			return nil
		default:
			return d.Fail("expected `,` or `}`")
		}
	}
}

func (d *jsonDecoder) Array(each func(i int) error) error {
	if d.peekByte() != '[' {
		return d.typeError("a sequence")
	}
	if err := d.enter(); err != nil {
		return err
	}
	d.pos++
	if d.peekByte() == ']' {
		d.pos++
		d.depth--
		return nil
	}
	for i := 0; ; i++ {
		if err := each(i); err != nil {
			return err
		}
		switch d.peekByte() {
		case ',':
			d.pos++
			if d.peekByte() == ']' {
				return d.Fail("trailing comma")
			}
		case ']':
			d.pos++
			d.depth--
			return nil
		default:
			return d.Fail("expected `,` or `]`")
		}
	}
}

// String reads a string value: no raw control characters, valid escapes, surrogates paired, and
// valid UTF-8 (serde_json checks the strings it decodes; it doesn't check those it skips).
func (d *jsonDecoder) String() (string, error) {
	if d.peekByte() != '"' {
		return "", d.typeError("a string")
	}
	d.pos++
	var out []byte
	start := d.pos
	for {
		if d.pos >= len(d.b) {
			return "", d.Fail("EOF while parsing a string")
		}
		c := d.b[d.pos]
		switch {
		case c == '"':
			out = append(out, d.b[start:d.pos]...)
			d.pos++
			if !utf8.Valid(out) {
				return "", d.Fail("invalid unicode code point")
			}
			return string(out), nil
		case c < 0x20:
			return "", d.Fail("control character (\\u0000-\\u001F) found while parsing a string")
		case c == '\\':
			out = append(out, d.b[start:d.pos]...)
			d.pos++
			if d.pos >= len(d.b) {
				return "", d.Fail("EOF while parsing a string")
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
					return "", d.Fail("lone leading surrogate in hex escape")
				case r >= 0xD800 && r <= 0xDBFF:
					if d.pos+2 > len(d.b) || d.b[d.pos] != '\\' || d.b[d.pos+1] != 'u' {
						return "", d.Fail("unexpected end of hex escape")
					}
					d.pos += 2
					r2, err := d.hex4()
					if err != nil {
						return "", err
					}
					if r2 < 0xDC00 || r2 > 0xDFFF {
						return "", d.Fail("lone leading surrogate in hex escape")
					}
					r = utf16.DecodeRune(r, r2)
				}
				out = utf8.AppendRune(out, r)
			default:
				return "", d.Fail("invalid escape")
			}
			start = d.pos
		default:
			d.pos++
		}
	}
}

func (d *jsonDecoder) hex4() (rune, error) {
	if d.pos+4 > len(d.b) {
		return 0, d.Fail("EOF while parsing a string")
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
			return 0, d.Fail("invalid escape")
		}
	}
	d.pos += 4
	return v, nil
}

// number reads a number's literal (RFC 8259 grammar: no leading zeros, '+' or bare '.').
func (d *jsonDecoder) number() (string, error) {
	if d.Peek() != KindNumber {
		return "", d.typeError("a number")
	}
	start := d.pos
	if err := d.scanNumber(); err != nil {
		return "", err
	}
	return string(d.b[start:d.pos]), nil
}

func (d *jsonDecoder) scanNumber() error {
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
		return d.Fail("EOF while parsing a value")
	}
	if d.b[d.pos] == '0' {
		d.pos++
		if d.pos < len(d.b) && d.b[d.pos] >= '0' && d.b[d.pos] <= '9' {
			return d.Fail("invalid number")
		}
	} else if digits() == 0 {
		return d.Fail("invalid number")
	}
	if d.pos < len(d.b) && d.b[d.pos] == '.' {
		d.pos++
		if digits() == 0 {
			return d.Fail("invalid number")
		}
	}
	if d.pos < len(d.b) && (d.b[d.pos] == 'e' || d.b[d.pos] == 'E') {
		d.pos++
		if d.pos < len(d.b) && (d.b[d.pos] == '+' || d.b[d.pos] == '-') {
			d.pos++
		}
		if digits() == 0 {
			return d.Fail("invalid number")
		}
	}
	return nil
}

func (d *jsonDecoder) Float(bits int) (float64, error) {
	lit, err := d.number()
	if err != nil {
		return 0, err
	}
	if bits == 32 {
		f, err := ParseF32(lit)
		if err != nil {
			return 0, d.Fail(err.Error())
		}
		return float64(f), nil
	}
	f, err := ParseF64(lit)
	if err != nil {
		return 0, d.Fail(err.Error())
	}
	return f, nil
}

func (d *jsonDecoder) Int(bits int) (int64, error) {
	lit, err := d.number()
	if err != nil {
		return 0, err
	}
	n, ok := intLiteral(lit, bits)
	if !ok {
		return 0, d.Fail("invalid value for a " + strconv.Itoa(bits) + "-bit integer: " + lit)
	}
	return n, nil
}

func (d *jsonDecoder) Uint(bits int) (uint64, error) {
	lit, err := d.number()
	if err != nil {
		return 0, err
	}
	n, ok := uintLiteral(lit, bits)
	if !ok {
		return 0, d.Fail("invalid value for an unsigned " + strconv.Itoa(bits) + "-bit integer: " + lit)
	}
	return n, nil
}

// Skip consumes the next value without decoding it, as serde_json ignores an unknown field:
// any depth (an explicit stack, not recursion), strings checked only for control characters and
// escape syntax.
func (d *jsonDecoder) Skip() error {
	var stack []byte
	for {
		switch c := d.peekByte(); {
		case c == 0:
			return d.Fail("EOF while parsing a value")
		case c == '{' || c == '[':
			d.pos++
			closing := byte('}')
			if c == '[' {
				closing = ']'
			}
			if d.peekByte() != closing {
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
			return d.Fail("expected value")
		}
		// After a value: close what ends here, or move to the next element.
		for next := false; !next; {
			if len(stack) == 0 {
				return nil
			}
			closing := stack[len(stack)-1]
			switch d.peekByte() {
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
				return d.Fail("expected `,` or `" + string(closing) + "`")
			}
		}
	}
}

func (d *jsonDecoder) skipKey() error {
	if d.peekByte() != '"' {
		return d.Fail("key must be a string")
	}
	if err := d.skipString(); err != nil {
		return err
	}
	if d.peekByte() != ':' {
		return d.Fail("expected `:`")
	}
	d.pos++
	return nil
}

// skipString consumes a string as serde_json's ignore_str: control characters and malformed
// escapes are errors; \u escapes need four hex digits but needn't pair up; bytes aren't checked.
func (d *jsonDecoder) skipString() error {
	d.pos++
	for {
		if d.pos >= len(d.b) {
			return d.Fail("EOF while parsing a string")
		}
		c := d.b[d.pos]
		d.pos++
		switch {
		case c == '"':
			return nil
		case c < 0x20:
			return d.Fail("control character (\\u0000-\\u001F) found while parsing a string")
		case c == '\\':
			if d.pos >= len(d.b) {
				return d.Fail("EOF while parsing a string")
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
				return d.Fail("invalid escape")
			}
		}
	}
}

// Raw consumes the next value and returns its text, checked only as a skipped value is (for
// fields whose type isn't modelled yet).
func (d *jsonDecoder) Raw() ([]byte, error) {
	d.skipSpace()
	start := d.pos
	if err := d.Skip(); err != nil {
		return nil, err
	}
	return d.b[start:d.pos], nil
}

func (d *jsonDecoder) Decode(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("serdejson: Decode needs a non-nil pointer")
	}
	return decodeValue(d, rv.Elem())
}

// UnitVariant reads "name", or {"name": null} as serde_json also accepts.
func (d *jsonDecoder) UnitVariant(names []string) (int, error) {
	switch d.Peek() {
	case KindString:
		return variantIndex(d, names)
	case KindObject:
		idx, entries := 0, 0
		err := d.Object(func(k Key) error {
			entries++
			if entries > 1 {
				return d.Fail("expected `}`")
			}
			i, err := variantIndex(&contentDecoder{cur: &Content{Kind: KindString, Str: k.Name}}, names)
			if err != nil {
				return d.Fail(err.Error())
			}
			idx = i
			null, err := d.Null()
			if err != nil {
				return err
			}
			if !null {
				return d.typeError("unit variant")
			}
			return nil
		})
		if err == nil && entries == 0 {
			return 0, d.Fail("expected value")
		}
		return idx, err
	}
	return 0, d.typeError("an enum")
}

// Tagged reads an internally tagged enum as serde does: the whole map (or sequence, tag first)
// is buffered as Content, the tag taken out of it, and the rest decoded from the Content.
func (d *jsonDecoder) Tagged(tag string, variants []string) (int, Decoder, error) {
	c, err := d.Content()
	if err != nil {
		return 0, nil, err
	}
	return taggedContent(&c, tag, variants, true)
}
