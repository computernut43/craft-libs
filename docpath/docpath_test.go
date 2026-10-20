package docpath

// Ported from crates/docpath/tests/docpath.rs: every case of every Rust test, plus the colon rules
// checked for both platforms on any OS (the Rust tests check only the platform they run on).

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	windows = platform{colonRefused: true, sep: '\\'}
	unix    = platform{colonRefused: false, sep: '/'}
)

func check(t *testing.T, pl platform, want Kind, cases []string) {
	t.Helper()
	for _, raw := range cases {
		if got := pl.classify(raw); got != want {
			t.Errorf("classify(%q) = %v, want %v", raw, got, want)
		}
	}
}

// checkHost also checks the exported API on the running platform.
func checkHost(t *testing.T, want Kind, cases []string) {
	t.Helper()
	check(t, host, want, cases)
	for _, raw := range cases {
		if got := IsLocal(raw); got != (want == Local) {
			t.Errorf("IsLocal(%q) = %v", raw, got)
		}
		if got := Classify(raw); got != want {
			t.Errorf("Classify(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestLocalPaths(t *testing.T) {
	checkHost(t, Local, []string{
		`C:\x`, "C:/x", `c:\Users\me\Pictures\a.png`, `\\?\C:\x`, `\\?\C:\`, `\\?\D:\long\folder\file.txt.`,
		"/home/u/x", "/", "img/a.png", `img\a.png`, "./a.png", "../shared/a.png", "C:x", "C:", `\x`,
		"console.txt", "nul_report.txt", "com10.txt", "lpt.txt", "auxiliary/a.png", "file:///C:/x/a.png",
		"file:///home/u/a.png", "file://localhost/C:/x", "FILE://LOCALHOST/home/u/x", "file:/C|/x",
		"file:///C:/My%20Pictures/a.png", "file:img/a.png", "file://C:/x", "file://c|/x", `file:\\C:\x`,
		"file:///C:/caf%E9.png", "file:///C:/%FF", `C:\\host\share`, `./\\host\share\x`, "D:/x", "z:",
		"file:///D:", `\\?\Z:\x`,
	})
}

func TestNetworkPaths(t *testing.T) {
	checkHost(t, Network, []string{
		`\\h\s`, `\\h\s\x.png`, "//h/s/x.png", `\/h\s`, `/\h\s`, `\\h`, `\\`, `\\\h\s`, `\\?\UNC\h\s\x`,
		`\\?\unc\h\s`, "//?/UNC/h/s/x", `\\.\UNC\h\s\x`, `\\h@SSL\s\x`, `\\h@80\s\x`, `\\h@SSL@443\DavWWWRoot\x`,
		"file://h/s/x.png", "file:////h/s/x.png", `file:\\h\s\x.png`, "file:%5C%5Ch%5Cs%5Cx.png",
		"file:///%5C%5Ch/s/x.png", "file://%5C%5Ch/s", "file:%2F%2Fh/s/x",
	})
}

func TestDevicePaths(t *testing.T) {
	checkHost(t, Device, []string{
		`\\.\C:`, `\\.\PhysicalDrive0`, `\\.\pipe\x`, "//./pipe/x", `\\.`, `\??\C:\x`, "/??/C:/x", `\??`, `\\?`,
		`\\?\`, `\\?\C:`, `\\?\C:x`, `\\?\GLOBALROOT\Device\HarddiskVolume1\x`,
		`\\?\Volume{12345678-1234-1234-1234-123456789abc}\x`, "//?/C:/x", `\/?\C:\x`, `/\?\C:\x`, `\\?/C:\x`,
		"CON", "con.txt", "Nul", "nul.tar.gz", "PRN...", "aux .png", "COM1", "com9.log", "COM¹", "LPT1",
		"lpt9.txt", "CONIN$", "conout$", "NUL:", `C:\docs\nul`, "img/CON/a.png", `\\?\C:\docs\con.txt`,
		"file:///C:/nul",
	})
}

func TestURLLinks(t *testing.T) {
	checkHost(t, URL, []string{
		"http://h/x.png", "https://h/x.png", "HTTPS://h/x.png", "ms-word:ofe|u|https://h/x.docx", "smb://h/s/x",
		"webdav://h/x", "mailto:a@b", "svn+ssh://h/x", "file:http://h/x", `http:\\h\x`, "data:image/png;base64,AAAA",
		"javascript:alert(1)", "VBScript:x", "ms-excel:ofe|u|https://h/x.xlsx", `search-ms:query=x&crumb=location:\\h\s`,
		"shell:startup", "tel:123", "news:comp.lang.rust", "jar:file:///C:/x.jar!/a.png", "view-source:http://h/x",
		"view-source:x", "JAR:x.jar!/a.png", "file:jar:x", "mailto:a", "Data:2024/a.png", "ms-settings:x",
		"MS-Word:x", "svn+ssh:h/x", "git+https:h/x", "x-app:https://h/x", "x-app:ofe|u|https://h/x",
		"http:/h/x", "shell:", "ab:", "ab:/x",
	})
}

// Start with a scheme and a colon but don't look like a URL.
var schemeLikeNames = []string{"name:stream", "chapter1:intro.png", "x.png:stream", "ab:c", "a1+b-c.d:x", "Shoot: Day 1/a.jpg", "notes:v2.pdf", "Photos:2024/a.jpg"}

// A colon inside a name other than a leading drive.
var colonNames = []string{
	"..:", ".:x", `C:\a\..:x`, `\\?\C:\a\..:x`, "a/b:c.png", "img/fig:1.png", `C:\x:stream`, `C:\a\.b:c`, "C:x:y",
	"C::x", "/home/u/a:b", "file:///C:/a:b", "file:///C:/a%3Ab", `\\?\C:\a:b`, ":x", "1a:b", "./ab:c", "2024:05/a.jpg",
	"Photos/2024:05/a.jpg", "/Users/a/2024:05/x.jpg", "file:///Users/a/2024:05/x.jpg",
}

func TestColonsInNamesAreRefusedOnlyOnWindows(t *testing.T) {
	check(t, windows, URL, schemeLikeNames)
	check(t, windows, Invalid, colonNames)
	check(t, unix, Local, schemeLikeNames)
	check(t, unix, Local, colonNames)
	if runtime.GOOS == "windows" {
		checkHost(t, URL, schemeLikeNames)
		checkHost(t, Invalid, colonNames)
	} else {
		checkHost(t, Local, schemeLikeNames)
		checkHost(t, Local, colonNames)
	}
}

func TestInvalidPaths(t *testing.T) {
	checkHost(t, Invalid, []string{
		"", "a\x00b", "a\nb", "a\tb", "a\u007f", "a\u0085b", "file:", "file:///C:/a%00b", "file:///C:/a%0Ab",
		`C:\..\x`, `C:\a\..\..\x`, "/..", `\\?\C:\a\..\b`, `\\?\C:\a\.\b`, `\\?\C:\a/b`, `\\?\C:\a\\b`,
		`C:\a\.. \b`, `C:\a\...\b`, "a/. /b", "file://", "file://localhost", "file://?x", "file:#x", "file:///C:/%81",
		"a\uFF3Cb", "\\\uFF3Ch\\s", "a\uFF0Fb", "a\u2215b", "a\u29F5b", "file:///C:/a%EF%BC%BCb",
		// Go only: not UTF-8, which a Rust &str cannot be.
		"a\xffb", "\xc3",
	})
}

func slashed(p string) string { return strings.ReplaceAll(p, `\`, "/") }

func mustResolve(t *testing.T, pl platform, raw, doc string) string {
	t.Helper()
	p, r := pl.resolve(raw, doc)
	if r != 0 {
		t.Fatalf("resolve(%q, %q): %v", raw, doc, r)
	}
	return p
}

func wantRefused(t *testing.T, pl platform, raw, doc string, want Refused) {
	t.Helper()
	if p, r := pl.resolve(raw, doc); r != want {
		t.Errorf("resolve(%q, %q) = %q, %v; want %v", raw, doc, p, r, want)
	}
}

func TestResolveJoinsRelativePathsToTheDocumentFolder(t *testing.T) {
	for _, pl := range []platform{windows, unix} {
		doc := `C:\docs\report\a.doc`
		ok := func(raw string) string { return slashed(mustResolve(t, pl, raw, doc)) }
		eq := func(got, want string) {
			t.Helper()
			if got != want {
				t.Errorf("colonRefused=%v: got %q, want %q", pl.colonRefused, got, want)
			}
		}
		eq(mustResolve(t, pl, `img\x.png`, doc), `C:\docs\report\img\x.png`)
		eq(ok("img/x.png"), "C:/docs/report/img/x.png")
		eq(ok("./x.png"), "C:/docs/report/x.png")
		eq(ok("../shared/x.png"), "C:/docs/shared/x.png")
		eq(ok(`\x.png`), "C:/x.png")
		eq(ok("C:x.png"), "C:/docs/report/x.png")
		eq(ok("D:x.png"), "D:x.png")
		eq(ok(`D:\x.png`), "D:/x.png")
		wantRefused(t, pl, `..\..\..\x.png`, doc, ErrInvalid)

		unixDoc := "/home/u/docs/a.doc"
		eq(slashed(mustResolve(t, pl, "img/x.png", unixDoc)), "/home/u/docs/img/x.png")
		eq(slashed(mustResolve(t, pl, `img\x.png`, unixDoc)), "/home/u/docs/img/x.png")
		eq(slashed(mustResolve(t, pl, "../x.png", unixDoc)), "/home/u/x.png")

		eq(mustResolve(t, pl, "img/../x.png", `\\?\C:\docs\a.doc`), `\\?\C:\docs\x.png`)

		eq(ok(`C:..\x.png`), "C:/docs/x.png")
		eq(ok(`C:..\..\x.png`), "C:/x.png")
		wantRefused(t, pl, `C:..\..\x.png`, `C:\docs\a.doc`, ErrInvalid)
		eq(ok(`./\\host\share\x.png`), "C:/docs/report/host/share/x.png")
		eq(ok(`C:\\host\share`), "C:/host/share")
		wantRefused(t, pl, "jar:x.jar!/a.png", doc, ErrURL)
		if pl.colonRefused {
			wantRefused(t, pl, "chapter1:intro.png", doc, ErrURL)
			wantRefused(t, pl, "img/fig:1.png", doc, ErrInvalid)
		} else {
			eq(ok("chapter1:intro.png"), "C:/docs/report/chapter1:intro.png")
			eq(ok("img/fig:1.png"), "C:/docs/report/img/fig:1.png")
		}
		// The document's own name may contain colons.
		eq(slashed(mustResolve(t, pl, "x.png", "/home/u/a:b/c:d.doc")), "/home/u/a:b/x.png")

		eq(slashed(mustResolve(t, pl, "img/x.png", "")), "img/x.png")
		eq(slashed(mustResolve(t, pl, "../x.png", "")), "../x.png")
	}
}

func TestResolveAllowsOnlyTheDocumentShare(t *testing.T) {
	pl := host
	doc := `\\Server\Share\docs\a.doc`
	eq := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	eq(mustResolve(t, pl, `img\x.png`, doc), `\\Server\Share\docs\img\x.png`)
	eq(mustResolve(t, pl, `\x.png`, doc), `\\Server\Share\x.png`)
	eq(mustResolve(t, pl, `\\server\share\img\x.png`, doc), `\\server\share\img\x.png`)
	mustResolve(t, pl, "//SERVER/SHARE/img/x.png", doc)
	mustResolve(t, pl, `\\?\UNC\server\share\x.png`, doc)
	mustResolve(t, pl, "file://localhost//server/share/x.png", doc)

	wantRefused(t, pl, `\\other\share\x.png`, doc, ErrNetwork)
	wantRefused(t, pl, `\\server\other\x.png`, doc, ErrNetwork)
	wantRefused(t, pl, `\\server\share\..\other\x.png`, doc, ErrInvalid)
	wantRefused(t, pl, `..\..\..\x.png`, doc, ErrInvalid)
	wantRefused(t, pl, "file://server/share/x.png", doc, ErrNetwork)
	wantRefused(t, pl, `\\server\share\nul`, doc, ErrDevice)

	verbatim := `\\?\UNC\server\share\docs\a.doc`
	eq(mustResolve(t, pl, "img/x.png", verbatim), `\\?\UNC\server\share\docs\img\x.png`)
	mustResolve(t, pl, `\\server\share\x.png`, verbatim)

	wantRefused(t, pl, `\\server\share\x.png`, `C:\docs\a.doc`, ErrNetwork)
	wantRefused(t, pl, `\\server\share\x.png`, "", ErrNetwork)
}

func TestResolveFileURLs(t *testing.T) {
	pl := host
	doc := `C:\docs\a.doc`
	ok := func(raw string) string { return slashed(mustResolve(t, pl, raw, doc)) }
	for _, c := range [][2]string{
		{"file:///D:/My%20Pictures/x.png", "D:/My Pictures/x.png"},
		{"file:///D:/a%23b.png#frag", "D:/a#b.png"},
		{"file:img/x.png", "C:/docs/img/x.png"},
		{"file://C:/x.png", "C:/x.png"},
		{"file://d|/x.png", "d:/x.png"},
		{"file:///C:/caf%E9.png", "C:/café.png"},
		{"file:///C:/caf%C3%A9.png", "C:/café.png"},
		{"file:///C:/caf%E9/na%C3%AFve%80.png", "C:/café/naïve€.png"},
	} {
		if got := ok(c[0]); got != c[1] {
			t.Errorf("%q: got %q, want %q", c[0], got, c[1])
		}
	}
	if got := slashed(mustResolve(t, pl, "file:///home/u/x.png", "")); got != "/home/u/x.png" {
		t.Errorf("file:///home/u/x.png: got %q", got)
	}
	wantRefused(t, pl, "file://localhost", doc, ErrInvalid)
	wantRefused(t, pl, "file://", doc, ErrInvalid)
	wantRefused(t, pl, "file://host/s/x.png", doc, ErrNetwork)
	wantRefused(t, pl, "file:%5C%5Chost%5Cs%5Cx.png", doc, ErrNetwork)
	wantRefused(t, pl, "file:///%5C%5Chost/s/x.png", doc, ErrNetwork)
	wantRefused(t, pl, "file:////./pipe/x", doc, ErrDevice)
	wantRefused(t, pl, "https://host/x.png", doc, ErrURL)
}

func TestResolveRefusesDeviceDocumentsAndPaths(t *testing.T) {
	pl := host
	wantRefused(t, pl, "x.png", `\\?\GLOBALROOT\Device\X\a.doc`, ErrDevice)
	if got := mustResolve(t, pl, `C:\x.png`, `\\?\GLOBALROOT\Device\X\a.doc`); got != `C:\x.png` {
		t.Errorf("got %q", got)
	}
	wantRefused(t, pl, `\\.\pipe\x`, `C:\a.doc`, ErrDevice)
}

func TestResolveAbsoluteRefusesResultsThatAreNotAbsolute(t *testing.T) {
	for _, raw := range []string{"img/x.png", "../x.png", "x.png", "file:img/x.png"} {
		if _, err := Resolve(raw, ""); err != nil {
			t.Errorf("Resolve(%q): %v", raw, err)
		}
		if _, err := ResolveAbsolute(raw, ""); err != ErrInvalid {
			t.Errorf("ResolveAbsolute(%q) = %v, want ErrInvalid", raw, err)
		}
	}
	if _, err := ResolveAbsolute(`\\h\s\x.png`, ""); !errors.Is(err, ErrNetwork) {
		t.Errorf("share: %v", err)
	}
	if _, err := ResolveAbsolute("https://h/x.png", ""); !errors.Is(err, ErrURL) {
		t.Errorf("url: %v", err)
	}

	ok := func(raw, doc string) string {
		t.Helper()
		p, err := ResolveAbsolute(raw, doc)
		if err != nil {
			t.Fatalf("ResolveAbsolute(%q, %q): %v", raw, doc, err)
		}
		return slashed(p)
	}
	eq := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	win := `C:\docs\a.doc`
	if runtime.GOOS == "windows" {
		eq(ok("img/x.png", win), "C:/docs/img/x.png")
		eq(ok(`C:\x.png`, ""), "C:/x.png")
		eq(ok("file:///C:/x.png", ""), "C:/x.png")
		eq(ok(`\\?\C:\x.png`, ""), "//?/C:/x.png")
		for _, c := range [][2]string{{`\x.png`, ""}, {"C:x.png", ""}, {"D:x.png", win}} {
			if _, err := ResolveAbsolute(c[0], c[1]); err != ErrInvalid {
				t.Errorf("ResolveAbsolute(%q, %q) = %v, want ErrInvalid", c[0], c[1], err)
			}
		}
	} else {
		unixDoc := "/home/u/docs/a.doc"
		eq(ok("img/x.png", unixDoc), "/home/u/docs/img/x.png")
		eq(ok("../x.png", unixDoc), "/home/u/x.png")
		eq(ok("file:///home/u/x.png", ""), "/home/u/x.png")
		eq(ok("/x.png", ""), "/x.png")
		catalog := "/Users/a/Pictures/Lightroom/cat.lrcat"
		eq(ok("../2024:05/a.jpg", catalog), "/Users/a/Pictures/2024:05/a.jpg")
		eq(ok("/Users/a/2024:05/x.jpg", ""), "/Users/a/2024:05/x.jpg")
		if _, err := ResolveAbsolute("view-source:x", catalog); err != ErrURL {
			t.Errorf("view-source: %v", err)
		}
		if _, err := ResolveAbsolute("img/x.png", win); err != ErrInvalid {
			t.Errorf("Windows document on Unix: %v", err)
		}
		if _, err := ResolveAbsolute(`C:\x.png`, ""); err != ErrInvalid {
			t.Errorf("Windows path on Unix: %v", err)
		}
	}
}

func TestRefusalsHaveMessagesAndKinds(t *testing.T) {
	kinds := map[Refused]Kind{ErrNetwork: Network, ErrDevice: Device, ErrURL: URL, ErrInvalid: Invalid}
	for r, k := range kinds {
		if r.Error() == "" {
			t.Errorf("%d: empty message", r)
		}
		if r.Kind() != k {
			t.Errorf("%v.Kind() = %v, want %v", r, r.Kind(), k)
		}
	}
	for k, s := range map[Kind]string{Local: "local", Network: "network", Device: "device", URL: "url", Invalid: "invalid"} {
		if k.String() != s {
			t.Errorf("%d.String() = %q", k, k.String())
		}
	}
}

// Go only: a document path that isn't UTF-8 (possible on Unix) still resolves relative links
// next to it, as Rust's non-UTF-8 Path does.
func TestNonUTF8Document(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("document paths that aren't UTF-8 exist on Unix only")
	}
	doc := "/home/u/caf\xe9/a.doc"
	if got := mustResolve(t, unix, "img/x.png", doc); got != "/home/u/caf\xe9/img/x.png" {
		t.Errorf("got %q", got)
	}
	if got := mustResolve(t, unix, "../x.png", doc); got != "/home/u/x.png" {
		t.Errorf("got %q", got)
	}
	wantRefused(t, unix, "../../../../x.png", doc, ErrInvalid)
	if got := mustResolve(t, unix, "x.png", "caf\xe9.doc"); got != "x.png" {
		t.Errorf("relative document: got %q", got)
	}
}

func TestEqFoldASCIIIgnoresUnicodeFolding(t *testing.T) {
	// The Kelvin sign folds to k under strings.EqualFold; Rust's eq_ignore_ascii_case doesn't.
	if !strings.EqualFold("\u212Aey", "key") || eqFoldASCII("\u212Aey", "key") {
		t.Fatal("eqFoldASCII must fold ASCII only")
	}
	if host.classify("LPT\u212A") != Local {
		t.Fatal("non-ASCII lookalikes are not device names")
	}
}

// FuzzResolve: hostile stored paths never panic, and what comes back is consistent.
func FuzzResolve(f *testing.F) {
	for _, s := range []string{`C:\x`, `\\h\s\x`, "file:///C:/caf%E9.png", `\\?\UNC\h\s`, "COM¹", "a/../..", "%", "file:%", `\\?\C:\a`} {
		f.Add(s, `C:\docs\a.doc`)
		f.Add(s, "")
	}
	f.Fuzz(func(t *testing.T, raw, doc string) {
		for _, pl := range []platform{windows, unix} {
			p, r := pl.resolve(raw, doc)
			if r != 0 {
				if p != "" {
					t.Fatalf("refused with a path: %q", p)
				}
				continue
			}
			// Only a document path that isn't UTF-8 can give an empty result ("." next to a bare
			// file name), as Rust's Path::parent does; ResolveAbsolute refuses it. Found by fuzzing.
			if p == "" && utf8.ValidString(doc) {
				t.Fatalf("resolve(%q, %q) gave an empty path", raw, doc)
			}
			// Refused characters never come from the link; the document's own path is trusted, as
			// in Rust (found by fuzzing: a control character in the document path is kept).
			if !strings.ContainsFunc(doc, isBadChar) && strings.ContainsFunc(p, isBadChar) {
				t.Fatalf("resolve(%q, %q) = %q contains a refused character", raw, doc, p)
			}
			if doc == "" && pl.classify(raw) != Local {
				t.Fatalf("resolved without a document but not Local: %q", raw)
			}
		}
	})
}
