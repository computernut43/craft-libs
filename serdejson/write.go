package serdejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Marshal encodes v with encoding/json and writes it as serde_json::to_vec_pretty would.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Pretty(raw)
}

// MarshalCompact encodes v and writes it as serde_json::to_vec would.
func MarshalCompact(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Compact(raw)
}

// Pretty rewrites JSON in serde_json's pretty layout (two-space indent, "key": value) with its
// string escaping. Numbers are kept as written.
func Pretty(data []byte) ([]byte, error) { return rewrite(data, true) }

// Compact rewrites JSON in serde_json's compact form with its string escaping.
func Compact(data []byte) ([]byte, error) { return rewrite(data, false) }

// maxWriteDepth bounds nesting while rewriting (encoding/json's own limit is 10,000).
const maxWriteDepth = 10_000

func rewrite(data []byte, pretty bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	w := &writer{pretty: pretty}
	if err := w.value(dec, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("serdejson: trailing data")
	}
	return w.out.Bytes(), nil
}

type writer struct {
	out    bytes.Buffer
	pretty bool
}

func (w *writer) newline(depth int) {
	if !w.pretty {
		return
	}
	w.out.WriteByte('\n')
	for range depth {
		w.out.WriteString("  ")
	}
}

func (w *writer) value(dec *json.Decoder, depth int) error {
	if depth > maxWriteDepth {
		return errors.New("serdejson: nesting too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		closing := byte(']')
		if t == '{' {
			closing = '}'
		}
		w.out.WriteByte(byte(t))
		first := true
		for dec.More() {
			if !first {
				w.out.WriteByte(',')
			}
			first = false
			w.newline(depth + 1)
			if t == '{' {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok {
					return errors.New("serdejson: object key is not a string")
				}
				writeString(&w.out, s)
				w.out.WriteByte(':')
				if w.pretty {
					w.out.WriteByte(' ')
				}
			}
			if err := w.value(dec, depth+1); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		if !first {
			w.newline(depth)
		}
		w.out.WriteByte(closing)
	case string:
		writeString(&w.out, t)
	case json.Number:
		w.out.WriteString(string(t))
	case bool:
		if t {
			w.out.WriteString("true")
		} else {
			w.out.WriteString("false")
		}
	case nil:
		w.out.WriteString("null")
	}
	return nil
}

// writeString writes s quoted with serde_json's escapes: \" \\ \b \t \n \f \r, other control
// characters as \u00xx, everything else as it is.
func writeString(out *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			if c < utf8.RuneSelf {
				out.WriteByte(c)
				i++
				continue
			}
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				// Rust strings are UTF-8; write what encoding/json would have.
				out.WriteString("�")
			} else {
				out.WriteString(s[i : i+n])
			}
			i += n
			continue
		}
		switch c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			out.WriteString(`\u00`)
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&0xF])
		}
		i++
	}
	out.WriteByte('"')
}
