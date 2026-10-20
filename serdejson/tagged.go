package serdejson

// AdjacentlyTagged reads an adjacently tagged enum (#[serde(tag = "…", content = "…")]) as
// serde's derived Deserialize does. From a map: other keys are skipped; the tag is read like a
// unit variant ("name" or {"name": null}); content that comes before the tag is buffered as
// Content and decoded from it; a second tag or content is an error, and so is missing content
// unless the variant is a unit (isUnit). From a sequence: [variant, content], exactly.
// decode reads variant i's content from d. It returns the variant's index.
func AdjacentlyTagged(d Decoder, tag, content string, variants []string, isUnit func(i int) bool, decode func(i int, d Decoder) error) (int, error) {
	switch d.Peek() {
	case KindObject:
	case KindArray:
		n := 0
		variant := -1
		err := d.Array(func(i int) error {
			n = i + 1
			switch i {
			case 0:
				v, err := seqVariant(d, variants)
				variant = v
				return err
			case 1:
				return decode(variant, d)
			}
			return d.Fail("trailing characters")
		})
		if err != nil {
			return 0, err
		}
		if n < 2 {
			return 0, d.Fail("invalid length, expected adjacently tagged enum")
		}
		return variant, nil
	default:
		return 0, d.Fail("invalid type, expected adjacently tagged enum")
	}
	variant := -1
	var buffered *Content
	sawContent := false
	err := d.Object(func(k Key) error {
		switch {
		case k.Name == tag:
			if variant >= 0 {
				return d.Fail("duplicate field `" + tag + "`")
			}
			v, err := d.UnitVariant(variants)
			if err != nil {
				return err
			}
			variant = v
			if buffered != nil {
				return decode(variant, &contentDecoder{cur: buffered})
			}
			return nil
		case k.Name == content:
			if sawContent {
				return d.Fail("duplicate field `" + content + "`")
			}
			sawContent = true
			if variant >= 0 {
				return decode(variant, d)
			}
			c, err := d.Content()
			if err != nil {
				return err
			}
			buffered = &c
			return nil
		}
		return d.Skip()
	})
	if err != nil {
		return 0, err
	}
	switch {
	case variant < 0:
		return 0, d.Fail("missing field `" + tag + "`")
	case !sawContent && !isUnit(variant):
		return 0, d.Fail("missing field `" + content + "`")
	}
	return variant, nil
}

// seqVariant reads the variant identifier at the start of a tagged sequence: from JSON text or a
// Value a string only (serde_json reads identifiers as strings); from Content an index works too.
func seqVariant(d Decoder, variants []string) (int, error) {
	cd, isContent := d.(*contentDecoder)
	if (!isContent || cd.fromValue) && d.Peek() != KindString {
		return 0, d.Fail("invalid type, expected variant identifier")
	}
	return variantIndex(d, variants)
}

// ExternallyTagged reads an externally tagged enum (serde's default enum representation) as
// serde's derived Deserialize does: "name" for a unit variant, or a map with exactly one entry,
// {"name": value}, where a unit variant's value is null (from Content, {} too). decode reads
// variant i's value from d (it isn't called for unit variants). It returns the variant's index.
func ExternallyTagged(d Decoder, variants []string, isUnit func(i int) bool, decode func(i int, d Decoder) error) (int, error) {
	switch d.Peek() {
	case KindString:
		i, err := variantIndex(d, variants)
		if err != nil {
			return 0, err
		}
		if !isUnit(i) {
			return 0, d.Fail("invalid type: unit variant, expected " + variants[i] + " variant")
		}
		return i, nil
	case KindObject:
		entries, variant := 0, 0
		err := d.Object(func(k Key) error {
			entries++
			if entries > 1 {
				return d.Fail("invalid value: map, expected map with a single key")
			}
			i, err := variantIndex(&contentDecoder{cur: &Content{Kind: KindString, Str: k.Name}}, variants)
			if err != nil {
				return d.Fail(err.Error())
			}
			variant = i
			if isUnit(i) {
				return unitValue(d)
			}
			return decode(i, d)
		})
		if err != nil {
			return 0, err
		}
		if entries == 0 {
			return 0, d.Fail("invalid value: map, expected map with a single key")
		}
		return variant, nil
	}
	return 0, d.Fail("invalid type, expected enum")
}

// unitValue reads a unit variant's value: null, or from serde's Content also {}.
func unitValue(d Decoder) error {
	if null, err := d.Null(); null || err != nil {
		return err
	}
	if cd, ok := d.(*contentDecoder); ok && !cd.fromValue && cd.cur != nil && cd.cur.Kind == KindObject && len(cd.cur.Keys) == 0 {
		return nil
	}
	return d.Fail("invalid type, expected unit")
}
