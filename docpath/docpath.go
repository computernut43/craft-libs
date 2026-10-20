// Package docpath decides whether a file path or file URL stored in a document may be opened,
// read or written. It is the Go port of the craft-docpath Rust crate (crates/docpath).
//
// Paths stored in documents (linked images, templates, data sources, media) are only followed
// when they refer to a local file, or to a file on the same network share the document itself was
// opened from. Paths to other computers, device paths and non-file URLs are refused, so opening a
// document never makes the app contact a server or device the user did not choose.
//
// The check is lexical: it does no I/O and, apart from colons, gives the same answer on every
// platform. Windows path forms (C:\, \\server\share, \\?\, \\.\, reserved names such as NUL)
// are recognised on all platforms, and both / and \ count as separators.
//
//   - [Classify] sorts a stored path into a [Kind].
//   - [IsLocal] is Classify(raw) == Local.
//   - [ResolveAbsolute] turns a stored path into the absolute path to open, relative to the
//     document, or a [Refused] error that says why it may not be opened. Use it to open files.
//   - [Resolve] is the same without the absolute check, for callers that only display or compare
//     the result.
//
// # Colons
//
// A colon after a drive letter at the start (C:\, C:/, C:x, \\?\C:\, file:///C:/) names a drive
// on every platform. Other colons depend on the platform:
//
//   - URL-shaped (see below) — https://h/x, mailto:a@b, jar:…, view-source:… — is [URL] everywhere.
//   - Any other scheme and a colon (chapter1:intro.png, notes:v2.pdf) is [URL] on Windows and
//     allowed on macOS and Linux.
//   - A colon inside any other name (2024:05/a.jpg) is [Invalid] on Windows and allowed on macOS
//     and Linux, where Finder shows a / in a name as : in the POSIX path.
//
// A stored path is URL-shaped when it starts with an RFC 3986 scheme of two or more characters and
// a colon, and either its first component (before the first / or \) ends with a colon, or the
// scheme, or one of its +-separated parts, compared ignoring ASCII case, starts with "ms-" or is
// one of about, blob, data, file, ftp, ftps, http, https, jar, javascript, mailto, news, nntp,
// search-ms, sftp, shell, smb, sms, ssh, tel, vbscript, view-source, webdav, ws or wss.
//
// # Caller contract
//
//   - Pass the document whenever there is one. Without it, a relative, root-relative or
//     drive-relative result depends on the process working directory, which can be a network
//     share; [ResolveAbsolute] refuses such a result.
//   - Hand the result to the filesystem as is (the os package uses wide Windows APIs); do not
//     percent-decode or normalise it again.
//   - On Windows, when the document was opened from a mapped drive, pass its UNC path instead if
//     the app can get it, so links on the same share resolve as that share.
//   - A lexical check cannot see mapped drives, subst drives, symlinks or junctions.
//
// Differences from the Rust crate: a stored path that is not valid UTF-8 (which a Rust &str cannot
// be) is [Invalid]; an empty document path means "no document".
package docpath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind says what a stored path refers to.
type Kind int

const (
	// Local is a file on this computer: absolute, drive-relative, root-relative or relative.
	Local Kind = iota
	// Network is a file on another computer: \\host\share, //host/share, \\?\UNC\…, file://host/….
	Network
	// Device is a device or object-manager path (\\.\…, \??\…, \\?\GLOBALROOT\…) or a reserved
	// name such as NUL or COM1.
	Device
	// URL is a URL other than file:, such as https://…, mailto:, jar:… or view-source:…; on
	// Windows also anything else that starts with a scheme of two or more characters and a colon.
	URL
	// Invalid is not a usable path: empty, not UTF-8, containing control characters or lookalike
	// separators, a name made of dots, or climbing above its root with ..; on Windows also a colon
	// inside a name.
	Invalid
)

func (k Kind) String() string {
	switch k {
	case Local:
		return "local"
	case Network:
		return "network"
	case Device:
		return "device"
	case URL:
		return "url"
	}
	return "invalid"
}

// Refused is why [Resolve] refused a stored path. Compare with errors.Is or ==.
type Refused int

const (
	// ErrNetwork: the path is on another computer, or on a network share other than the document's.
	ErrNetwork Refused = iota + 1
	// ErrDevice: the path names a device rather than a file.
	ErrDevice
	// ErrURL: the link is a URL, not a file path.
	ErrURL
	// ErrInvalid: the path is not usable.
	ErrInvalid
)

func (r Refused) Error() string {
	switch r {
	case ErrNetwork:
		return "the path is on another computer"
	case ErrDevice:
		return "the path names a device, not a file"
	case ErrURL:
		return "the link is not a file path"
	}
	return "the path is not valid"
}

// Kind is the Kind a refusal corresponds to.
func (r Refused) Kind() Kind {
	switch r {
	case ErrNetwork:
		return Network
	case ErrDevice:
		return Device
	case ErrURL:
		return URL
	}
	return Invalid
}

// platform holds what depends on the running system, so tests can check every platform's rules.
type platform struct {
	colonRefused bool // Windows: colons in names are refused
	sep          byte // os.PathSeparator
}

var host = platform{colonRefused: runtime.GOOS == "windows", sep: os.PathSeparator}

// Classify classifies a path or URL stored in a document, without a document to resolve it
// against. Relative paths are Local; a rooted path whose .. components climb above its drive,
// share or / is Invalid.
func Classify(raw string) Kind { return host.classify(raw) }

// IsLocal reports whether a path or URL stored in a document refers to a local file.
func IsLocal(raw string) bool { return Classify(raw) == Local }

// ResolveAbsolute turns a path or URL stored in a document into the absolute path to open, or
// refuses it. This is the function to call before opening, reading or writing the file.
//
// It is [Resolve] followed by a check that the result is absolute on the running platform
// (filepath.IsAbs); a result that is not, such as a relative link in a document that has no path
// yet, is refused with ErrInvalid. On Unix, Windows-form results (C:\x, \\server\share\x) are not
// absolute and are refused too.
func ResolveAbsolute(raw, document string) (string, error) {
	p, err := Resolve(raw, document)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		return "", ErrInvalid
	}
	return p, nil
}

// Resolve turns a path or URL stored in a document into the path to open, or refuses it with a
// [Refused] error. The result may be relative; to open a file, use [ResolveAbsolute].
//
// document is the path of the document the link came from, or "" when it has none. The steps:
//
//  1. A file: URL is percent-decoded (as Windows-1252 where the escapes are not UTF-8) and its
//     scheme removed. Only an empty host, localhost or a drive (file://C:/x) is local;
//     file://host/… is ErrNetwork and a URL with no path ErrInvalid.
//  2. A relative path is joined to the document's folder, a root-relative path (\x) to the
//     document's drive or share, and a drive-relative path (C:x) to the document's folder when
//     the document is on that drive. Without a document they are returned unresolved.
//  3. . and .. are folded lexically; climbing above a drive, share or / is ErrInvalid.
//  4. A network path is allowed only on the document's own share (same \\server\share, compared
//     ignoring ASCII case). Other network paths, device paths and reserved names are refused.
//
// Windows-form results (drive, share, \\?\) use \ as the separator on every platform; others use
// the platform separator.
func Resolve(raw, document string) (string, error) {
	p, r := host.resolve(raw, document)
	if r != 0 {
		return "", r
	}
	return p, nil
}

func (pl platform) classify(raw string) Kind {
	if _, r := pl.resolve(raw, ""); r != 0 {
		return r.Kind()
	}
	return Local
}

type rootKind int

const (
	rootRelative      rootKind = iota // a\b
	rootRooted                        // \a or /a
	rootDriveRelative                 // C:a
	rootDrive                         // C:\a
	rootVerbatimDrive                 // \\?\C:\a
	rootShare                         // \\host\share\a, or \\?\UNC\host\share\a when verbatim
)

type root struct {
	kind        rootKind
	drive       byte // drive letter for the drive kinds
	host, share string
	verbatim    bool
}

func (r root) isAbsolute() bool { return r.kind != rootRelative && r.kind != rootDriveRelative }

func (r root) driveLetter() (byte, bool) {
	if r.kind == rootDrive || r.kind == rootVerbatimDrive {
		return r.drive, true
	}
	return 0, false
}

type parsed struct {
	root  root
	comps []string
}

func isSep(c rune) bool { return c == '/' || c == '\\' }

func isASCIIAlpha(c rune) bool { return c < utf8.RuneSelf && ('a' <= c|0x20 && c|0x20 <= 'z') }

// eqFoldASCII compares ignoring ASCII case only (Rust eq_ignore_ascii_case), unlike
// strings.EqualFold, which also folds non-ASCII letters such as the Kelvin sign.
func eqFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// splitSep splits s at its first separator, like Rust's split_once(is_sep).
func splitSep(s string) (string, string, bool) {
	if i := strings.IndexAny(s, `/\`); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

func (pl platform) resolve(raw, document string) (string, Refused) {
	plain, r := pl.stripURL(raw)
	if r != 0 {
		return "", r
	}
	link, r := pl.parse(plain, true)
	if r != 0 {
		return "", r
	}

	hasDoc := document != ""
	docUTF8 := hasDoc && utf8.ValidString(document)
	if hasDoc && !docUTF8 && link.root.kind == rootRelative {
		return joinNative(document, link.comps)
	}
	var doc parsed
	var docErr Refused
	if docUTF8 {
		doc, docErr = pl.parse(document, false)
	}
	docShare := docUTF8 && docErr == 0 && doc.root.kind == rootShare

	rt, comps := link.root, link.comps
	switch {
	case link.root.kind == rootRelative && docUTF8:
		if docErr != 0 {
			return "", docErr
		}
		rt, comps = doc.root, append(folder(doc.comps), link.comps...)
	case link.root.kind == rootRooted && docUTF8 && docErr == 0 && doc.root.isAbsolute():
		rt = doc.root
	case link.root.kind == rootDriveRelative && docUTF8 && docErr == 0:
		if d, ok := doc.root.driveLetter(); ok && eqFoldASCII(string(d), string(link.root.drive)) {
			rt, comps = doc.root, append(folder(doc.comps), link.comps...)
		}
	}
	comps, r = normalize(rt, comps)
	if r != 0 {
		return "", r
	}
	if rt.kind == rootShare {
		same := docShare && eqFoldASCII(doc.root.host, rt.host) && eqFoldASCII(doc.root.share, rt.share)
		if !same {
			return "", ErrNetwork
		}
	}
	return pl.render(rt, comps), 0
}

// folder is the document's folder: its components without the file name (a copy).
func folder(comps []string) []string {
	if len(comps) == 0 {
		return nil
	}
	return append([]string(nil), comps[:len(comps)-1]...)
}

// stripURL rejects control characters and non-file URLs and reduces a file: URL to its path.
func (pl platform) stripURL(raw string) (string, Refused) {
	if raw == "" || !utf8.ValidString(raw) || strings.ContainsFunc(raw, isBadChar) {
		return "", ErrInvalid
	}
	path := raw
	if len(raw) >= 5 && eqFoldASCII(raw[:5], "file:") {
		p, r := fileURLPath(raw[5:])
		if r != 0 {
			return "", r
		}
		path = p
	}
	if hasScheme(path) {
		scheme, _, _ := strings.Cut(path, ":")
		// NUL: and COM1: name devices, not URL schemes.
		if isDOSDevice(scheme) {
			return "", ErrDevice
		}
		if pl.colonRefused || isURLShaped(path) {
			return "", ErrURL
		}
	}
	return path, 0
}

// urlSchemes are refused on every platform even when the rest doesn't look like a URL (compared
// ignoring ASCII case); any scheme starting with "ms-" is refused too.
var urlSchemes = map[string]bool{
	"about": true, "blob": true, "data": true, "file": true, "ftp": true, "ftps": true, "http": true, "https": true,
	"jar": true, "javascript": true, "mailto": true, "news": true, "nntp": true, "search-ms": true, "sftp": true,
	"shell": true, "smb": true, "sms": true, "ssh": true, "tel": true, "vbscript": true, "view-source": true,
	"webdav": true, "ws": true, "wss": true,
}

func isURLShaped(s string) bool {
	scheme, _, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	first, _, _ := splitSep(s)
	if strings.HasSuffix(first, ":") {
		return true
	}
	for _, part := range strings.Split(scheme, "+") {
		part = lowerASCII(part)
		if strings.HasPrefix(part, "ms-") || urlSchemes[part] {
			return true
		}
	}
	return false
}

// isBadChar: control characters (Unicode Cc, as Rust char::is_control), and separator lookalikes
// that code page best-fit mappings turn into \ or / when a path reaches an ANSI API.
func isBadChar(c rune) bool {
	return unicode.IsControl(c) || c == '\uFF3C' || c == '\uFF0F' || c == '\u2215' || c == '\u29F5'
}

// hasScheme: starts with an RFC 3986 scheme (a letter, then letters, digits, +, - or .) of two or
// more characters, and a colon; so a drive letter (C:) is not one.
func hasScheme(s string) bool {
	scheme, _, ok := strings.Cut(s, ":")
	if !ok || len(scheme) < 2 || !isASCIIAlpha(rune(scheme[0])) {
		return false
	}
	for _, c := range scheme[1:] {
		if !(c < utf8.RuneSelf && (isASCIIAlpha(c) || ('0' <= c && c <= '9') || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}

// isDriveSpec reports whether s is exactly a letter followed by ':' or '|'.
func isDriveSpec(s string) (byte, bool) {
	if len(s) == 2 && isASCIIAlpha(rune(s[0])) && (s[1] == ':' || s[1] == '|') {
		return s[0], true
	}
	return 0, false
}

// fileURLPath is the path of a file: URL (the part after "file:").
func fileURLPath(rest string) (string, Refused) {
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	decoded := percentDecode(rest)
	if decoded == "" || strings.ContainsFunc(decoded, isBadChar) {
		return "", ErrInvalid
	}
	path := decoded
	if len(decoded) >= 2 && isSep(rune(decoded[0])) && isSep(rune(decoded[1])) {
		after := decoded[2:]
		host, p := after, ""
		if i := strings.IndexAny(after, `/\`); i >= 0 {
			host, p = after[:i], after[i:]
		}
		// file://C:/x and file://C|/x name the drive path C:/x.
		if l, ok := isDriveSpec(host); ok {
			return string(l) + ":" + p, 0
		}
		if host != "" && !eqFoldASCII(host, "localhost") {
			return "", ErrNetwork
		}
		if p == "" {
			return "", ErrInvalid
		}
		path = p
	}
	// /C:/x and /C|/x name the drive path C:/x.
	if len(path) >= 3 && isSep(rune(path[0])) {
		if l, ok := isDriveSpec(path[1:3]); ok && (len(path) == 3 || isSep(rune(path[3]))) {
			return string(l) + ":" + path[3:], 0
		}
	}
	return path, 0
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// percentDecode decodes a file: URL path. A run of escapes that is not UTF-8 is read as
// Windows-1252, which older Windows software used in file URLs.
func percentDecode(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	var run []byte
	flush := func() {
		if utf8.Valid(run) {
			out.Write(run)
		} else {
			for _, b := range run {
				out.WriteRune(windows1252(b))
			}
		}
		run = run[:0]
	}
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			h, okH := unhex(s[i+1])
			l, okL := unhex(s[i+2])
			if okH && okL {
				run = append(run, h<<4|l)
				i += 3
				continue
			}
		}
		flush()
		_, size := utf8.DecodeRuneInString(s[i:])
		out.WriteString(s[i : i+size])
		i += size
	}
	flush()
	return out.String()
}

// windows1252High maps 0x80–0x9F; 0x81, 0x8D, 0x8F, 0x90 and 0x9D are unassigned and map to C1
// controls, which are refused.
var windows1252High = [32]rune{
	'€', '\u0081', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '\u008D', 'Ž', '\u008F',
	'\u0090', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '\u009D', 'ž', 'Ÿ',
}

func windows1252(b byte) rune {
	if b >= 0x80 && b <= 0x9F {
		return windows1252High[b-0x80]
	}
	return rune(b)
}

// parse splits a plain path into its root and components. check applies the component rules
// (reserved device names, dot-and-space names, and colons on Windows); the document's own path
// is not checked.
func (pl platform) parse(s string, check bool) (parsed, Refused) {
	first, n1 := utf8.DecodeRuneInString(s)
	second, n2 := utf8.DecodeRuneInString(s[n1:])
	rest := s[n1+n2:]
	switch {
	case s == "":
		comps, r := pl.comps(s, check)
		return parsed{root: root{kind: rootRelative}, comps: comps}, r
	case isSep(first) && n2 > 0 && isSep(second):
		return pl.parseDouble(s, check)
	case isSep(first):
		after := s[1:]
		if r, ok := strings.CutPrefix(after, "??"); ok {
			if c, _ := utf8.DecodeRuneInString(r); r == "" || isSep(c) {
				return parsed{}, ErrDevice
			}
		}
		comps, e := pl.comps(after, check)
		return parsed{root: root{kind: rootRooted}, comps: comps}, e
	case isASCIIAlpha(first) && n2 > 0 && second == ':':
		if c, n := utf8.DecodeRuneInString(rest); n > 0 && isSep(c) {
			comps, e := pl.comps(rest[n:], check)
			return parsed{root: root{kind: rootDrive, drive: byte(first)}, comps: comps}, e
		}
		comps, e := pl.comps(rest, check)
		return parsed{root: root{kind: rootDriveRelative, drive: byte(first)}, comps: comps}, e
	}
	comps, e := pl.comps(s, check)
	return parsed{root: root{kind: rootRelative}, comps: comps}, e
}

// parseDouble handles a path starting with two separators: a share, or a \\?\, \\.\ or //?/ prefix.
func (pl platform) parseDouble(s string, check bool) (parsed, Refused) {
	t := s[2:]
	first, after, _ := splitSep(t)
	if first != "?" && first != "." {
		return pl.parseShare(t, false, check)
	}
	if v, ok := strings.CutPrefix(s, `\\?\`); ok {
		return pl.parseVerbatim(v, check)
	}
	// \\.\, and \\?\ spelled with any /, which Windows normalises into the device namespace.
	next, rest, _ := splitSep(after)
	if eqFoldASCII(next, "UNC") {
		return pl.parseShare(rest, false, check)
	}
	return parsed{}, ErrDevice
}

// parseVerbatim handles \\?\ paths: only \, and no normalisation, so ., .. and / are not usable.
func (pl platform) parseVerbatim(v string, check bool) (parsed, Refused) {
	first, rest, hasRest := strings.Cut(v, `\`)
	// \\?\C: without a separator is the volume, not its root folder.
	if hasRest && len(first) == 2 && isASCIIAlpha(rune(first[0])) && first[1] == ':' {
		comps, r := pl.verbatimComps(rest, check)
		return parsed{root: root{kind: rootVerbatimDrive, drive: first[0]}, comps: comps}, r
	}
	if hasRest && eqFoldASCII(first, "UNC") {
		return pl.parseShare(rest, true, check)
	}
	return parsed{}, ErrDevice
}

// parseShare handles host\share\rest, the part after the leading separators (or \\?\UNC\).
func (pl platform) parseShare(t string, verbatim, check bool) (parsed, Refused) {
	split := func(s string) (string, string) {
		if verbatim {
			a, b, _ := strings.Cut(s, `\`)
			return a, b
		}
		a, b, _ := splitSep(s)
		return a, b
	}
	h, rest := split(t)
	sh, rest := split(rest)
	if h == "" || sh == "" {
		return parsed{}, ErrNetwork
	}
	var comps []string
	var r Refused
	if verbatim {
		comps, r = pl.verbatimComps(rest, check)
	} else {
		comps, r = pl.comps(rest, check)
	}
	return parsed{root: root{kind: rootShare, host: h, share: sh, verbatim: verbatim}, comps: comps}, r
}

func (pl platform) comps(s string, check bool) ([]string, Refused) {
	var out []string
	for _, c := range strings.FieldsFunc(s, isSep) {
		if check {
			if isDOSDevice(c) {
				return nil, ErrDevice
			}
			if isDotName(c) || pl.hasRefusedColon(c) {
				return nil, ErrInvalid
			}
		}
		out = append(out, c)
	}
	return out, 0
}

func (pl platform) verbatimComps(s string, check bool) ([]string, Refused) {
	parts := strings.Split(s, `\`)
	last := len(parts) - 1
	out := make([]string, 0, len(parts))
	for i, c := range parts {
		if c == "" && i == last {
			continue
		}
		if c == "" || c == "." || c == ".." || strings.Contains(c, "/") {
			return nil, ErrInvalid
		}
		if check && isDOSDevice(c) {
			return nil, ErrDevice
		}
		if check && (isDotName(c) || pl.hasRefusedColon(c)) {
			return nil, ErrInvalid
		}
		out = append(out, c)
	}
	return out, 0
}

// hasRefusedColon: a colon inside a name, which only Windows refuses.
func (pl platform) hasRefusedColon(comp string) bool {
	return pl.colonRefused && strings.Contains(comp, ":")
}

// isDotName: a name of dots and spaces other than . and ..; Windows trims trailing dots and
// spaces, so ".. " or "..." could act as "..".
func isDotName(comp string) bool {
	if comp == "" || comp == "." || comp == ".." {
		return false
	}
	return strings.Trim(comp, ". ") == ""
}

var dosDevices = []string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$"}

// isDOSDevice: a reserved DOS device name, with or without an extension, stream or trailing dots
// and spaces.
func isDOSDevice(comp string) bool {
	stem := comp
	if i := strings.IndexAny(comp, ".:"); i >= 0 {
		stem = comp[:i]
	}
	stem = strings.TrimRight(stem, " ")
	for _, n := range dosDevices {
		if eqFoldASCII(stem, n) {
			return true
		}
	}
	if len(stem) < 3 || !(eqFoldASCII(stem[:3], "COM") || eqFoldASCII(stem[:3], "LPT")) {
		return false
	}
	tail := stem[3:]
	c, n := utf8.DecodeRuneInString(tail)
	if n == 0 || n != len(tail) {
		return false
	}
	return ('0' <= c && c <= '9') || c == '¹' || c == '²' || c == '³'
}

// normalize folds . and ..; a rooted path may not climb above its root, a relative one keeps
// leading ...
func normalize(r root, comps []string) ([]string, Refused) {
	out := make([]string, 0, len(comps))
	for _, c := range comps {
		switch c {
		case ".":
		case "..":
			switch {
			case len(out) > 0 && out[len(out)-1] != "..":
				out = out[:len(out)-1]
			case r.isAbsolute():
				return nil, ErrInvalid
			default:
				out = append(out, c)
			}
		default:
			out = append(out, c)
		}
	}
	return out, 0
}

func (pl platform) render(r root, comps []string) string {
	native := string(pl.sep)
	var prefix, sep string
	switch r.kind {
	case rootRelative:
		if len(comps) == 0 {
			prefix = "."
		}
		sep = native
	case rootRooted:
		prefix, sep = native, native
	case rootDriveRelative:
		prefix, sep = string(r.drive)+":", `\`
	case rootDrive:
		prefix, sep = string(r.drive)+`:\`, `\`
	case rootVerbatimDrive:
		prefix, sep = `\\?\`+string(r.drive)+`:\`, `\`
	case rootShare:
		if r.verbatim {
			prefix = `\\?\UNC\` + r.host + `\` + r.share + `\`
		} else {
			prefix = `\\` + r.host + `\` + r.share + `\`
		}
		sep = `\`
	}
	return prefix + strings.Join(comps, sep)
}

// joinNative resolves a relative link next to a document whose path is not UTF-8 (possible on
// Unix only), like Rust's PathBuf parent/pop/push.
func joinNative(document string, comps []string) (string, Refused) {
	p := parent(document)
	for _, c := range comps {
		switch c {
		case ".":
		case "..":
			if p == "" || p == "/" {
				return "", ErrInvalid
			}
			p = parent(p)
		default:
			if p == "" {
				p = c
			} else {
				p = filepath.Join(p, c)
			}
		}
	}
	return p, 0
}

// parent is the path without its last element ("" when there is none), like Rust Path::parent.
func parent(p string) string {
	d := filepath.Dir(p)
	if d == "." {
		return ""
	}
	return d
}
