package docpath

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMatchesRust compares this package with the Rust crate on a generated corpus of hostile
// stored paths and documents. It runs when CRAFT_DOCPATH_RUST names the reference program built
// from testdata/rust-oracle (`cargo build --release` there), on the same OS, so the platform
// rules agree.
func TestMatchesRust(t *testing.T) {
	bin := os.Getenv("CRAFT_DOCPATH_RUST")
	if bin == "" {
		t.Skip("set CRAFT_DOCPATH_RUST to testdata/rust-oracle's release binary to compare with Rust")
	}
	if !filepath.IsAbs(bin) {
		t.Fatalf("CRAFT_DOCPATH_RUST must be absolute: %q", bin)
	}
	corpus := diffCorpus(60000)
	var in bytes.Buffer
	for _, c := range corpus {
		fmt.Fprintf(&in, "%s\t%s\n", hex.EncodeToString([]byte(c[0])), hex.EncodeToString([]byte(c[1])))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = &in
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	mismatches := 0
	for i := 0; sc.Scan(); i++ {
		if i >= len(corpus) {
			t.Fatal("reference printed more lines than inputs")
		}
		raw, doc := corpus[i][0], corpus[i][1]
		if got, want := goLine(raw, doc), sc.Text(); got != want {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("raw %q doc %q:\n  go   %s\n  rust %s", raw, doc, got, want)
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d cases differ from Rust", mismatches, len(corpus))
	}
	t.Logf("%d cases match Rust", len(corpus))
}

var rustKind = map[Kind]string{Local: "Local", Network: "Network", Device: "Device", URL: "Url", Invalid: "Invalid"}
var rustRefused = map[Refused]string{ErrNetwork: "Network", ErrDevice: "Device", ErrURL: "Url", ErrInvalid: "Invalid"}

func goLine(raw, doc string) string {
	res := ""
	if p, err := Resolve(raw, doc); err != nil {
		r, _ := err.(Refused)
		res = "err:" + rustRefused[r]
	} else {
		res = "ok:" + hex.EncodeToString([]byte(p))
	}
	return rustKind[Classify(raw)] + "\t" + res
}

// diffTokens are the pieces paths are built from: separators, prefixes, schemes, escapes, device
// names, dots, colons, lookalikes and non-ASCII letters.
var diffTokens = []string{
	`\`, "/", `\\`, "//", "?", ".", "..", "...", ". ", ".. ", ":", "|", "C:", "c|", "z:", "UNC", "unc", "GLOBALROOT",
	"file:", "FILE:", "file://", "file:///", "localhost", "LOCALHOST", "host", "share", "Server", "@SSL", "@80",
	"%5C", "%2F", "%2f", "%E9", "%C3%A9", "%81", "%80", "%00", "%0A", "%3A", "%EF%BC%BC", "%", "%G1", "#", "?x",
	"con", "NUL", "nul.txt", "COM1", "com9", "COM¹", "lpt3", "LPT", "CONIN$", "conout$", "aux .png", "prn...",
	"a", "b.png", "img", "x", " ", "http", "https:", "ms-word:", "ms-", "svn+ssh", "jar:", "view-source:", "data",
	"Data:", "mailto:", "x-app:", "ab:", "1a", "a1+b-c.d", "chapter1:", "notes:v2", "\u212A", "é", "\uFF3C", "\u2215",
	"\t", "\u0085", "Volume{1}", "PhysicalDrive0", "pipe", "??", "\u00b2",
}

var diffDocs = []string{
	"", `C:\docs\report\a.doc`, `\\Server\Share\docs\a.doc`, `\\?\UNC\server\share\docs\a.doc`, `\\?\C:\docs\a.doc`,
	"/home/u/docs/a.doc", `\\?\GLOBALROOT\Device\X\a.doc`, "/home/u/a:b/c:d.doc", "rel/a.doc", `D:x\a.doc`, "/",
}

// diffCorpus builds n deterministic (raw, document) pairs: the hand-written test cases first,
// then random token strings against the fixed documents and against random documents.
func diffCorpus(n int) [][2]string {
	var out [][2]string
	known := [][]string{schemeLikeNames, colonNames}
	for _, group := range known {
		for _, raw := range group {
			for _, d := range diffDocs {
				out = append(out, [2]string{raw, d})
			}
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	gen := func() string {
		var b strings.Builder
		for k := 1 + rng.IntN(8); k > 0; k-- {
			b.WriteString(diffTokens[rng.IntN(len(diffTokens))])
		}
		return b.String()
	}
	for len(out) < n {
		doc := diffDocs[rng.IntN(len(diffDocs))]
		if rng.IntN(4) == 0 {
			doc = gen()
		}
		out = append(out, [2]string{gen(), doc})
	}
	return out
}
