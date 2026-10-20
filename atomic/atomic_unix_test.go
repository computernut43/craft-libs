//go:build unix

package atomic

// Unix-only tests ported from crates/atomic: modes, umask, symlinks, read-only targets and the
// copy fallback's permission handling.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func mode(t *testing.T, p string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func fileMode(f *os.File) fs.FileMode {
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	return info.Mode().Perm()
}

// canWriteReadOnly: root may write a 0444 file anyway, so read-only checks mean nothing then.
func canWriteReadOnly(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "probe")
	writeFile(t, p, nil)
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err == nil {
		f.Close()
	}
	os.Remove(p)
	return err == nil
}

func TestExclusiveCopyStartsPrivateAndKeepsCallbackMode(t *testing.T) {
	d := t.TempDir()
	for _, m := range []fs.FileMode{0o600, 0o640} {
		p := filepath.Join(d, fmt.Sprintf("export-%o.craft", m))
		f := &faulty{noHardLinks: true}
		err := create(f, p, Private, func(file *os.File) error {
			if _, err := file.WriteString("private export"); err != nil {
				return err
			}
			return file.Chmod(m)
		})
		if err != nil {
			t.Fatal(err)
		}
		// Both the staging file and the exclusive-copy destination start private.
		if !slices.Equal(f.createdModes, []fs.FileMode{0o600, 0o600}) {
			t.Fatalf("created modes %o", f.createdModes)
		}
		if got := mode(t, p); got != m {
			t.Fatalf("mode %o, want %o", got, m)
		}
		assertContent(t, p, []byte("private export"))
	}
	assertNoTemp(t, d)
}

func TestFailingToKeepPermissionsFailsTheSave(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	f := &faulty{failPermissions: true}
	err := opts(f, noRetry).Write(p, []byte("new"))
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("message: %v", err)
	}
	assertContent(t, p, original)
	if f.renames != 0 {
		t.Fatal("renamed")
	}
	assertNoTemp(t, d)
}

// umaskChild runs one test in a child process under a real umask: changing the umask inside
// parallel tests would race (the Rust tests do the same through sh).
func umaskChild(t *testing.T, test, mask string, env ...string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `umask "$1"; exec "$2" -test.run="^$3\$" -test.v -test.count=1`, "umask-child", mask, exe, test)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("umask %s: %v\n%s", mask, err, out)
	}
	if !strings.Contains(string(out), "--- PASS: "+test) {
		t.Fatalf("umask %s: the child must run its test:\n%s", mask, out)
	}
}

func TestNormalCreationNeedsNoChmodEvenWhenHardLinksFail(t *testing.T) {
	for _, mask := range []string{"000", "002", "022", "027", "077"} {
		umaskChild(t, "TestNormalCreationNeedsNoChmodChild", mask, "CRAFT_ATOMIC_COPY_CHILD=1")
	}
}

func TestNormalCreationNeedsNoChmodChild(t *testing.T) {
	if os.Getenv("CRAFT_ATOMIC_COPY_CHILD") == "" {
		t.Skip("run by TestNormalCreationNeedsNoChmodEvenWhenHardLinksFail under each umask")
	}
	d := t.TempDir()
	reference := filepath.Join(d, "reference")
	writeFile(t, reference, []byte("normal creation"))
	// writeFile asks for 0644; ordinary creation in this test asks for 0666 like the library.
	os.Remove(reference)
	ref, err := os.OpenFile(reference, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	ref.Close()
	expected := mode(t, reference)
	for _, noHardLinks := range []bool{false, true} {
		p := filepath.Join(d, fmt.Sprintf("export-%v", noHardLinks))
		f := &faulty{noHardLinks: noHardLinks, normalCollisions: 2, failPermissions: true}
		err := create(f, p, PlatformDefault, func(file *os.File) error {
			if got := fileMode(file); got != expected {
				return fmt.Errorf("stage mode %o, want %o", got, expected)
			}
			_, err := file.WriteString("document payload")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != expected {
			t.Fatalf("mode %o, want %o", got, expected)
		}
		assertContent(t, p, []byte("document payload"))
		for _, m := range f.createdModes {
			if m != expected {
				t.Fatalf("stage and copy must match ordinary creation: %o", f.createdModes)
			}
		}
		if slices.Contains(f.events, "set_permissions") {
			t.Fatal("no chmod for normal creation")
		}
	}
	p := filepath.Join(d, "new-document")
	f := &faulty{failPermissions: true}
	if err := opts(f, noRetry).Write(p, []byte("document")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, p); got != expected {
		t.Fatalf("mode %o, want %o", got, expected)
	}
	if slices.Contains(f.events, "set_permissions") {
		t.Fatal("no chmod for a new document")
	}
	assertNoTemp(t, d)
}

func TestNormalPolicyPreservesExistingModesWithPrivateStaging(t *testing.T) {
	d := t.TempDir()
	for _, m := range []fs.FileMode{0o600, 0o640, 0o664} {
		p := filepath.Join(d, fmt.Sprintf("existing-%o", m))
		writeFile(t, p, original)
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
		f := &faulty{failNormalCreate: true}
		if err := replace(f, noRetry, p, PlatformDefault, fillWith("replacement")); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != m {
			t.Fatalf("mode %o, want %o", got, m)
		}
		if slices.Contains(f.events, "create_new_normal") {
			t.Fatal("replacements stage privately")
		}
		if !slices.Equal(f.createdModes, []fs.FileMode{0o600}) {
			t.Fatalf("created modes %o", f.createdModes)
		}
	}
	assertNoTemp(t, d)
}

func TestNormalPermissionFailuresAbortBeforePublication(t *testing.T) {
	d := t.TempDir()
	for _, exclusive := range []bool{false, true} {
		for _, c := range []struct {
			tag          string
			fs           *faulty
			kind         error
			callbackRuns bool
		}{
			{"create", &faulty{failNormalCreate: true}, fs.ErrPermission, false},
			{"collisions", &faulty{normalCollisions: 8}, fs.ErrExist, false},
			{"sync", &faulty{failSync: true}, diskFull, true},
		} {
			p := filepath.Join(d, fmt.Sprintf("%s-%v", c.tag, exclusive))
			called := false
			fill := func(f *os.File) error {
				called = true
				_, err := f.WriteString("payload")
				return err
			}
			var err error
			if exclusive {
				err = create(c.fs, p, PlatformDefault, fill)
			} else {
				err = replace(c.fs, noRetry, p, PlatformDefault, fill)
			}
			if !errors.Is(err, c.kind) {
				t.Fatalf("%s: %v, want %v", c.tag, err, c.kind)
			}
			if called != c.callbackRuns {
				t.Fatalf("%s: callback ran = %v", c.tag, called)
			}
			assertMissing(t, p)
			assertNoTemp(t, d)
		}
	}
}

func TestNormalCopyPreservesCallbackModesAndRefusesPermissionFailures(t *testing.T) {
	d := t.TempDir()
	for _, m := range []fs.FileMode{0o600, 0o640, 0o660} {
		for _, noHardLinks := range []bool{false, true} {
			p := filepath.Join(d, fmt.Sprintf("%o-%v", m, noHardLinks))
			f := &faulty{noHardLinks: noHardLinks}
			err := create(f, p, PlatformDefault, func(file *os.File) error {
				if err := file.Chmod(m); err != nil {
					return err
				}
				_, err := file.WriteString("callback payload")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := mode(t, p); got != m {
				t.Fatalf("mode %o, want %o", got, m)
			}
			assertContent(t, p, []byte("callback payload"))
			for _, n := range f.permissionLengths {
				if n != 0 {
					t.Fatal("normal copies must be narrowed before any payload is written")
				}
			}
		}
	}
	p := filepath.Join(d, "permission-failure")
	f := &faulty{noHardLinks: true, failPermissions: true}
	err := create(f, p, PlatformDefault, func(file *os.File) error {
		// Toggle a bit from ordinary creation so the copy really needs a mode change.
		if err := file.Chmod(fileMode(file) ^ 0o040); err != nil {
			return err
		}
		_, err := file.WriteString("must not survive")
		return err
	})
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want a permission error: %v", err)
	}
	if !slices.Equal(f.permissionLengths, []int64{0}) {
		t.Fatalf("refuse before copying payload: %v", f.permissionLengths)
	}
	assertMissing(t, p)
	assertNoTemp(t, d)
}

// From tests/atomic_tests.rs.

func TestModeAndSymlinkPreservation(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "secret.key")
	writeFile(t, p, original)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("updated secret")); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, p); got != 0o600 {
		t.Fatalf("mode %o", got)
	}

	real, link := filepath.Join(d, "real.txt"), filepath.Join(d, "link.txt")
	writeFile(t, real, original)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("through symlink")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the link must stay a link")
	}
	assertContent(t, real, []byte("through symlink"))
}

func TestDanglingRelativeSymlinkStaysALink(t *testing.T) {
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link.txt")
	if err := os.Symlink("real/doc.txt", link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("created through the link")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the link must stay a link")
	}
	assertContent(t, filepath.Join(d, "real", "doc.txt"), []byte("created through the link"))
	assertNoTemp(t, d)
	assertNoTemp(t, filepath.Join(d, "real"))
}

func TestSymlinkLoopIsAnError(t *testing.T) {
	d := t.TempDir()
	a := filepath.Join(d, "a")
	if err := os.Symlink("b", a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(d, "b")); err != nil {
		t.Fatal(err)
	}
	err := Write(a, []byte("x"))
	if err == nil || !strings.HasPrefix(err.Error(), "cannot save") || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("message: %v", err)
	}
	if err := WriteNew(a, []byte("x")); err == nil {
		t.Fatal("WriteNew through a loop: want an error")
	}
	if info, err := os.Lstat(a); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the link must stay a link")
	}
	assertNoTemp(t, d)
}

func TestReadOnlyTargetIsNotReplaced(t *testing.T) {
	d := t.TempDir()
	if canWriteReadOnly(t, d) {
		t.Skip("running as a user who may write read-only files (root)")
	}
	p := filepath.Join(d, "locked.txt")
	writeFile(t, p, original)
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	err := Write(p, []byte("new"))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("message: %v", err)
	}
	assertContent(t, p, original)
	assertNoTemp(t, d)
}

func TestPrivateStagingPreservesExistingModesAfterCallback(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []fs.FileMode{0o600, 0o640, 0o664, 0o200} {
		p := filepath.Join(d, fmt.Sprintf("doc-%o.craft", m))
		writeFile(t, p, original)
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
		err := WriteFunc(p, func(f *os.File) error {
			if got := fileMode(f); got != 0o600 {
				return fmt.Errorf("stage must be private before the callback, even for a shared destination: %o", got)
			}
			// Settings writers restrict the stage themselves; that mustn't change the mode the
			// replacement promises to keep.
			if err := f.Chmod(0o600); err != nil {
				return err
			}
			if _, err := f.WriteString("new private payload"); err != nil {
				return err
			}
			if got := fileMode(f); got != 0o600 {
				return fmt.Errorf("payload must stay private while writing: %o", got)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != m {
			t.Fatalf("mode %o, want %o", got, m)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
		assertContent(t, p, []byte("new private payload"))
	}
	assertNoTemp(t, d)
}

func TestExplicitPrivateFilesKeepCallbackPermissions(t *testing.T) {
	d := t.TempDir()
	private := Options{NewFilePermissions: Private}
	for _, exclusive := range []bool{false, true} {
		p := filepath.Join(d, fmt.Sprintf("new-%v.craft", exclusive))
		fill := func(f *os.File) error {
			if got := fileMode(f); got != 0o600 {
				return fmt.Errorf("private stage %o", got)
			}
			if _, err := f.WriteString("new payload"); err != nil {
				return err
			}
			// A new file has no existing mode to restore, so the callback may select it.
			return f.Chmod(0o640)
		}
		var err error
		if exclusive {
			err = private.WriteNewFunc(p, fill)
		} else {
			err = private.WriteFunc(p, fill)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != 0o640 {
			t.Fatalf("mode %o", got)
		}
		assertContent(t, p, []byte("new payload"))
	}
	for _, exclusive := range []bool{false, true} {
		p := filepath.Join(d, fmt.Sprintf("default-%v.craft", exclusive))
		var err error
		if exclusive {
			err = private.WriteNew(p, []byte("explicitly private"))
		} else {
			err = private.Write(p, []byte("explicitly private"))
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != 0o600 {
			t.Fatalf("mode %o", got)
		}
	}
	assertNoTemp(t, d)
}

// From tests/permissions.rs.

func TestNormalCreationMatchesOSPermissions(t *testing.T) {
	for _, c := range [][2]string{{"000", "666"}, {"002", "664"}, {"022", "644"}, {"027", "640"}, {"077", "600"}} {
		umaskChild(t, "TestPermissionChild", c[0], "CRAFT_PERMISSION_TEST_DIR="+t.TempDir(), "CRAFT_PERMISSION_TEST_EXPECTED="+c[1])
	}
}

func TestPermissionChild(t *testing.T) {
	dir := os.Getenv("CRAFT_PERMISSION_TEST_DIR")
	if dir == "" {
		t.Skip("run by TestNormalCreationMatchesOSPermissions under each umask")
	}
	e, err := strconv.ParseUint(os.Getenv("CRAFT_PERMISSION_TEST_EXPECTED"), 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	expected := fs.FileMode(e)
	ordinary := filepath.Join(dir, "ordinary")
	f, err := os.OpenFile(ordinary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := mode(t, ordinary); got != expected {
		t.Fatalf("ordinary creation %o, want %o", got, expected)
	}
	var o Options
	payload := []byte("document bytes")
	for _, c := range []struct {
		tag                  string
		streaming, exclusive bool
	}{{"replace", false, false}, {"replace-stream", true, false}, {"exclusive", false, true}, {"exclusive-stream", true, true}} {
		path := filepath.Join(dir, c.tag)
		fill := func(f *os.File) error {
			if got := fileMode(f); got != expected {
				return fmt.Errorf("normal creation before writing: %o", got)
			}
			if _, err := f.Write(payload); err != nil {
				return err
			}
			if got := fileMode(f); got != expected {
				return fmt.Errorf("normal creation during writing: %o", got)
			}
			return nil
		}
		var err error
		switch {
		case !c.streaming && !c.exclusive:
			err = o.Write(path, payload)
		case !c.streaming && c.exclusive:
			err = o.WriteNew(path, payload)
		case c.streaming && !c.exclusive:
			err = o.WriteFunc(path, fill)
		default:
			err = o.WriteNewFunc(path, fill)
		}
		if err != nil {
			t.Fatalf("%s: %v", c.tag, err)
		}
		if got := mode(t, path); got != expected {
			t.Fatalf("%s: mode %o, want %o", c.tag, got, expected)
		}
		assertContent(t, path, payload)
		if os.Getenv("CRAFT_PERMISSION_TEST_ACL") == "1" {
			if got, want := acl(t, path), acl(t, ordinary); got != want {
				t.Fatalf("%s: inherited ACL %q, want %q", c.tag, got, want)
			}
		}
		if c.exclusive {
			if err := o.WriteNew(path, []byte("must not replace")); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("%s: %v", c.tag, err)
			}
			assertContent(t, path, payload)
		} else {
			if err := os.Chmod(path, 0o660); err != nil {
				t.Fatal(err)
			}
			if err := o.Write(path, []byte("replacement")); err != nil {
				t.Fatal(err)
			}
			if got := mode(t, path); got != 0o660 {
				t.Fatalf("%s: keep the existing mode regardless of umask: %o", c.tag, got)
			}
		}
	}
	for tag, write := range map[string]func(string, []byte) error{"default": Write, "default-exclusive": WriteNew} {
		p := filepath.Join(dir, tag)
		if err := write(p, []byte("document")); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != expected {
			t.Fatalf("%s: mode %o", tag, got)
		}
	}
	private := Options{NewFilePermissions: Private}
	for _, exclusive := range []bool{false, true} {
		p := filepath.Join(dir, fmt.Sprintf("private-%v", exclusive))
		fill := func(f *os.File) error {
			if fileMode(f)&0o077 != 0 {
				return fmt.Errorf("private from creation: %o", fileMode(f))
			}
			_, err := f.WriteString("private")
			return err
		}
		var err error
		if exclusive {
			err = private.WriteNewFunc(p, fill)
		} else {
			err = private.WriteFunc(p, fill)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, p); got != 0o600 {
			t.Fatalf("private: mode %o", got)
		}
	}
	for _, exclusive := range []bool{false, true} {
		path := filepath.Join(dir, fmt.Sprintf("callback-error-%v", exclusive))
		fill := func(f *os.File) error {
			if _, err := f.WriteString("partial"); err != nil {
				return err
			}
			return diskFull
		}
		var err error
		if exclusive {
			err = o.WriteNewFunc(path, fill)
		} else {
			err = o.WriteFunc(path, fill)
		}
		if !errors.Is(err, diskFull) {
			t.Fatalf("callback error: %v", err)
		}
		assertMissing(t, path)
	}
	assertNoTemp(t, dir)
}

// The Rust ACL tests are #[ignore]d (they need setfacl/getfacl and POSIX ACL support); here they
// run when CRAFT_ATOMIC_ACL=1.
func TestDirectoryDefaultACLTakesPrecedenceOverRestrictiveUmask(t *testing.T) {
	dir := t.TempDir()
	setDefaultACL(t, dir)
	// With this default ACL, ordinary creation yields 0640 even under umask 077.
	umaskChild(t, "TestPermissionChild", "077", "CRAFT_PERMISSION_TEST_DIR="+dir, "CRAFT_PERMISSION_TEST_EXPECTED=640", "CRAFT_PERMISSION_TEST_ACL=1")
}

// acl is a file's ACL as getfacl prints it, without the file name header.
func acl(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("getfacl", "-c", "-n", p).Output()
	if err != nil {
		t.Fatalf("getfacl %s: %v", p, err)
	}
	return string(out)
}

func setDefaultACL(t *testing.T, dir string) {
	t.Helper()
	if os.Getenv("CRAFT_ATOMIC_ACL") != "1" {
		t.Skip("set CRAFT_ATOMIC_ACL=1 on a file system with POSIX ACLs (needs setfacl/getfacl)")
	}
	out, err := exec.Command("setfacl", "-m", "d:u::rwx,d:u:12345:r--,d:g::r-x,d:m::r-x,d:o::---", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("setfacl: %v\n%s", err, out)
	}
}

// Rust: normal_permissions_follow_inherited_acl_with_hard_links_and_copy (#[ignore]d there).
func TestNormalPermissionsFollowInheritedACLWithHardLinksAndCopy(t *testing.T) {
	d := t.TempDir()
	setDefaultACL(t, d)
	reference := filepath.Join(d, "reference")
	writeFile(t, reference, []byte("normal creation"))
	expected := acl(t, reference)
	for _, noHardLinks := range []bool{false, true} {
		f := &faulty{noHardLinks: noHardLinks, failPermissions: true}
		p := filepath.Join(d, fmt.Sprintf("export-%v", noHardLinks))
		if err := create(f, p, PlatformDefault, fillWith("export")); err != nil {
			t.Fatal(err)
		}
		if got := acl(t, p); got != expected {
			t.Fatalf("noHardLinks=%v: ACL %q, want the ordinary creation's %q", noHardLinks, got, expected)
		}
		assertContent(t, p, []byte("export"))
	}
	assertNoTemp(t, d)
}
