package rustcompat

import (
	"strings"
	"unicode"
)

// CharToLowercase appends `c.to_lowercase()` to dst: Unicode's default lowercase mapping of one
// character, without context. It is the simple mapping (unicode.ToLower) except for the one
// unconditional SpecialCasing entry: İ (U+0130) becomes i followed by a combining dot above.
func CharToLowercase(dst []rune, c rune) []rune {
	if c == 'İ' {
		return append(dst, 'i', '̇')
	}
	return append(dst, unicode.ToLower(c))
}

// LowercaseChars is `s.chars().flat_map(char::to_lowercase)`, collected.
func LowercaseChars(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		out = CharToLowercase(out, c)
	}
	return out
}

// IsWhitespace is Rust's `char::is_whitespace`: the Unicode White_Space property.
func IsWhitespace(c rune) bool { return unicode.Is(unicode.White_Space, c) }

// Trim is Rust's `str::trim`: leading and trailing White_Space removed.
func Trim(s string) string { return strings.TrimFunc(s, IsWhitespace) }

// TrimStart is Rust's `str::trim_start`.
func TrimStart(s string) string { return strings.TrimLeftFunc(s, IsWhitespace) }

// TrimEnd is Rust's `str::trim_end`.
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsWhitespace) }
