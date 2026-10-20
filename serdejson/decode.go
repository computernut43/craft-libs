package serdejson

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

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

// decodeValue reads the next value from d into v (settable), driven by v's type.
func decodeValue(d Decoder, v reflect.Value) error {
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
		if err := decodeValue(d, p.Elem()); err != nil {
			return err
		}
		v.Set(p)
		return nil
	case reflect.Struct:
		return decodeStruct(d, v)
	case reflect.Map:
		return decodeMap(d, v)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 0, 0)
		err := d.Array(func(int) error {
			e := reflect.New(t.Elem()).Elem()
			if err := decodeValue(d, e); err != nil {
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
				return d.Fail(fmt.Sprintf("trailing characters (expected %d elements)", t.Len()))
			}
			n = i + 1
			return decodeValue(d, v.Index(i))
		})
		if err != nil {
			return err
		}
		if n != t.Len() {
			return d.Fail(fmt.Sprintf("invalid length %d, expected an array of length %d", n, t.Len()))
		}
		return nil
	case reflect.String:
		s, err := d.String()
		if err == nil {
			v.SetString(s)
		}
		return err
	case reflect.Bool:
		b, err := d.Bool()
		if err == nil {
			v.SetBool(b)
		}
		return err
	case reflect.Float32, reflect.Float64:
		f, err := d.Float(t.Bits())
		if err == nil {
			v.SetFloat(f)
		}
		return err
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := d.Int(t.Bits())
		if err == nil {
			v.SetInt(n)
		}
		return err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := d.Uint(t.Bits())
		if err == nil {
			v.SetUint(n)
		}
		return err
	}
	return errors.New("serdejson: can't decode into " + t.String())
}

func decodeStruct(d Decoder, v reflect.Value) error {
	t := v.Type()
	si := infoOf(t)
	fresh := reflect.New(t).Elem()
	if si.allDefault {
		setDefault(fresh)
	}
	seen := make([]bool, len(si.fields))
	switch d.Peek() {
	case KindObject:
		err := d.Object(func(k Key) error {
			fi, ok := si.byName[k.Name]
			if !ok {
				return d.Skip()
			}
			if seen[fi] {
				return d.Fail("duplicate field `" + k.Name + "`")
			}
			seen[fi] = true
			return decodeValue(d, fresh.FieldByIndex(si.fields[fi].index))
		})
		if err != nil {
			return err
		}
		for fi, f := range si.fields {
			if seen[fi] {
				continue
			}
			if !f.optional {
				return d.Fail("missing field `" + f.name + "`")
			}
			if f.tagged && !si.allDefault {
				setDefault(fresh.FieldByIndex(f.index))
			}
		}
	case KindArray:
		// serde takes a struct as the sequence of its fields in order.
		n := 0
		err := d.Array(func(i int) error {
			if i >= len(si.fields) {
				return d.Fail("trailing characters in sequence for struct " + t.Name())
			}
			n = i + 1
			return decodeValue(d, fresh.FieldByIndex(si.fields[i].index))
		})
		if err != nil {
			return err
		}
		for i := n; i < len(si.fields); i++ {
			f := si.fields[i]
			if !si.allDefault && !f.tagged {
				return d.Fail(fmt.Sprintf("invalid length %d, expected struct %s with %d elements", n, t.Name(), len(si.fields)))
			}
			if f.tagged && !si.allDefault {
				setDefault(fresh.FieldByIndex(f.index))
			}
		}
	default:
		return d.Fail("invalid type, expected struct " + t.Name())
	}
	v.Set(fresh)
	return nil
}

func setDefault(v reflect.Value) {
	if ds, ok := v.Addr().Interface().(DefaultSetter); ok {
		ds.SetDefaults()
	}
}

func decodeMap(d Decoder, v reflect.Value) error {
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
	err := d.Object(func(k Key) error {
		var key reflect.Value
		if numeric {
			var err error
			if key, err = k.numeric(kt); err != nil {
				return d.Fail(err.Error())
			}
		} else {
			key = reflect.New(kt).Elem()
			key.SetString(k.Name)
		}
		e := reflect.New(t.Elem()).Elem()
		if err := decodeValue(d, e); err != nil {
			return err
		}
		m.SetMapIndex(key, e)
		return nil
	})
	if err != nil {
		return err
	}
	v.Set(m)
	return nil
}
