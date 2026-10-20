package serdejson

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
)

// U32Map is a Rust BTreeMap<u32, V>: written with its keys in numeric order (encoding/json
// sorts them as strings). Unmarshal reads it with serde_json's rule for numeric keys (the key's
// text must be a JSON integer in range, with no escapes, leading zeros or spaces); a repeated
// key takes the later value.
type U32Map[V any] map[uint32]V

// MarshalJSON writes the entries in key order.
func (m U32Map[V]) MarshalJSON() ([]byte, error) {
	keys := make([]uint32, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strconv.FormatUint(uint64(k), 10))
		b.WriteString(`":`)
		v, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// StrMap is a Rust BTreeMap<String, V>: written in key order (as encoding/json sorts string
// keys) and as {} when empty or nil (encoding/json writes a nil map as null).
type StrMap[V any] map[string]V

// MarshalJSON writes the entries in key order.
func (m StrMap[V]) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]V(m))
}

// Vec is a Rust Vec<T>: written as [] when empty or nil (encoding/json writes a nil slice as
// null) and as numbers for bytes (encoding/json writes []byte as base64).
type Vec[T any] []T

// MarshalJSON writes the elements.
func (v Vec[T]) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		e, err := json.Marshal(x)
		if err != nil {
			return nil, err
		}
		b.Write(e)
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}
