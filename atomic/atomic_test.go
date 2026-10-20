package atomic

// Ported from crates/atomic/src/lib.rs (tests module) and crates/atomic/tests/atomic_tests.rs.
// Unix-only tests are in atomic_unix_test.go.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

var original = []byte("the user's precious original document")

// diskFull is the platform's "no space left" error, as a full disk reports it.
var diskFull error = syscall.ENOSPC

func init() {
	if runtime.GOOS == "windows" {
		diskFull = syscall.Errno(errorDiskFull)
	}
}

var errPermission = fmt.Errorf("injected: operation not permitted: %w", fs.ErrPermission)

// faulty is an FS that records events and injects failures (Rust: Faulty).
type faulty struct {
	events             []string
	createdModes       []fs.FileMode
	permissionLengths  []int64
	failSync           bool
	syncsBeforeFailure int // syncs fail once this many have succeeded; 0 = never
	syncs              int
	failRename         error
	lockedRenames      int
	renames            int
	noHardLinks        bool
	failPermissions    bool
	failSyncDir        bool
	failNormalCreate   bool
	normalCollisions   int
	createdMeanwhile   []byte // another process creates the target just before the hard link
}

func (f *faulty) CreateNew(path string, perm NewFilePermissions) (*os.File, error) {
	normal := perm == PlatformDefault
	if normal {
		f.events = append(f.events, "create_new_normal")
	} else {
		f.events = append(f.events, "create_new")
	}
	if normal && f.failNormalCreate {
		return nil, fmt.Errorf("injected: normal creation failed: %w", fs.ErrPermission)
	}
	if normal && f.normalCollisions > 0 {
		f.normalCollisions--
		return nil, fs.ErrExist
	}
	file, err := RealFS{}.CreateNew(path, perm)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err == nil {
		f.createdModes = append(f.createdModes, info.Mode().Perm())
	}
	return file, nil
}

func (f *faulty) Sync(file *os.File) error {
	f.events = append(f.events, "sync_file")
	done := f.syncs
	f.syncs++
	if f.failSync || (f.syncsBeforeFailure > 0 && done >= f.syncsBeforeFailure) {
		return fmt.Errorf("injected: no space left on device: %w", diskFull)
	}
	return RealFS{}.Sync(file)
}

func (f *faulty) Rename(from, to string) error {
	f.events = append(f.events, "rename")
	f.renames++
	if f.failRename != nil {
		return f.failRename
	}
	if f.lockedRenames > 0 {
		f.lockedRenames--
		return fmt.Errorf("injected: sharing violation: %w", fs.ErrPermission)
	}
	return RealFS{}.Rename(from, to)
}

func (f *faulty) HardLink(from, to string) error {
	f.events = append(f.events, "hard_link")
	if f.createdMeanwhile != nil {
		if err := os.WriteFile(to, f.createdMeanwhile, 0o644); err != nil {
			return err
		}
	}
	if f.noHardLinks {
		return fmt.Errorf("injected: hard links not supported: %w", fs.ErrPermission)
	}
	return RealFS{}.HardLink(from, to)
}

func (f *faulty) Remove(path string) error {
	f.events = append(f.events, "remove")
	return RealFS{}.Remove(path)
}

func (f *faulty) SyncDir(dir string) error {
	f.events = append(f.events, "sync_dir")
	if f.failSyncDir {
		return fmt.Errorf("injected: directory sync failed: %w", fs.ErrPermission)
	}
	return RealFS{}.SyncDir(dir)
}

func (f *faulty) SetPermissions(file *os.File, mode fs.FileMode) error {
	f.events = append(f.events, "set_permissions")
	if info, err := file.Stat(); err == nil {
		f.permissionLengths = append(f.permissionLengths, info.Size())
	}
	if f.failPermissions {
		return errPermission
	}
	return RealFS{}.SetPermissions(file, mode)
}

var noRetry = RenameRetry{}

func opts(f FS, retry RenameRetry) Options { return Options{FS: f, Retry: &retry} }

func read(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return data
}

func assertContent(t *testing.T, p string, want []byte) {
	t.Helper()
	if got := read(t, p); !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", filepath.Base(p), got, want)
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".craft_tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func assertMissing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists (%v)", p, err)
	}
}

func writeFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fillWith(data string) func(*os.File) error {
	return func(f *os.File) error {
		_, err := f.WriteString(data)
		return err
	}
}

func TestReplacesAndCreates(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	if err := Write(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("first"))
	if err := Write(p, []byte("second, longer")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("second, longer"))
	assertNoTemp(t, d)
}

func TestAMissingFolderIsAnErrorNamingIt(t *testing.T) {
	d := t.TempDir()
	folder := filepath.Join(d, "a")
	p := filepath.Join(folder, "b", "out.png")
	for _, err := range []error{Write(p, []byte("x")), WriteNew(p, []byte("x"))} {
		if err == nil || !strings.Contains(err.Error(), filepath.Join(folder, "b")) {
			t.Errorf("want an error naming the folder: %v", err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("want fs.ErrNotExist: %v", err)
		}
	}
	assertMissing(t, folder)
}

func TestFailedFlushKeepsOriginal(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	f := &faulty{failSync: true}
	err := opts(f, noRetry).Write(p, bytes.Repeat([]byte{7}, 4096))
	if !errors.Is(err, diskFull) {
		t.Fatalf("want a disk-full error: %v", err)
	}
	if !strings.Contains(err.Error(), "disk is full") || !strings.Contains(err.Error(), "original file was not changed") {
		t.Fatalf("message: %v", err)
	}
	assertContent(t, p, original)
	if f.renames != 0 {
		t.Fatalf("renamed %d times", f.renames)
	}
	assertNoTemp(t, d)
}

func TestStreamingWritesGoThroughTheInjectedFS(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	chunks := func(f *os.File) error {
		if _, err := f.WriteString("chunk1 "); err != nil {
			return err
		}
		_, err := f.WriteString("chunk2")
		return err
	}
	if err := opts(&faulty{failSync: true}, noRetry).WriteFunc(p, chunks); err == nil {
		t.Fatal("failed sync: want an error")
	}
	f := &faulty{failRename: errors.New("injected rename failure")}
	if err := opts(f, noRetry).WriteFunc(p, chunks); err == nil {
		t.Fatal("failed rename: want an error")
	}
	if f.renames != 1 {
		t.Fatalf("renames = %d", f.renames)
	}
	assertContent(t, p, original)
	assertNoTemp(t, d)
	if err := opts(&faulty{}, noRetry).WriteFunc(p, chunks); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("chunk1 chunk2"))
}

func TestRenameFailureKeepsOriginal(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	if err := opts(&faulty{failRename: errors.New("injected rename failure")}, noRetry).Write(p, []byte("new")); err == nil {
		t.Fatal("want an error")
	}
	assertContent(t, p, original)
	assertNoTemp(t, d)
}

func TestLockedTargetRenameIsRetried(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	f := &faulty{lockedRenames: 2}
	if err := opts(f, RenameRetry{Attempts: 3, FirstDelay: time.Millisecond}).Write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("new"))
	if f.renames != 3 {
		t.Fatalf("renames = %d", f.renames)
	}
	assertNoTemp(t, d)
}

func TestOptionsInjectPolicyFailuresAndRetryBudget(t *testing.T) {
	d := t.TempDir()
	for _, policy := range []NewFilePermissions{Private, PlatformDefault} {
		for _, streaming := range []bool{false, true} {
			path := filepath.Join(d, fmt.Sprintf("%v-%v", policy, streaming))
			f := &faulty{lockedRenames: 1}
			o := Options{NewFilePermissions: policy, FS: f, Retry: &RenameRetry{Attempts: 1}}
			var err error
			if streaming {
				err = o.WriteFunc(path, fillWith("payload"))
			} else {
				err = o.Write(path, []byte("payload"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.renames != 2 {
				t.Fatalf("renames = %d", f.renames)
			}
			assertContent(t, path, []byte("payload"))
			event := "create_new_normal"
			if policy == Private {
				event = "create_new"
			}
			if f.events[0] != event {
				t.Fatalf("%v: first event %q, want %q", policy, f.events[0], event)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			f = &faulty{failSync: true}
			o.FS = f
			if streaming {
				err = o.WriteNewFunc(path, fillWith("payload"))
			} else {
				err = o.WriteNew(path, []byte("payload"))
			}
			if !errors.Is(err, diskFull) {
				t.Fatalf("want disk full: %v", err)
			}
			if f.events[0] != event {
				t.Fatalf("%v: first event %q, want %q", policy, f.events[0], event)
			}
			assertMissing(t, path)
			assertNoTemp(t, d)
		}
	}
}

func TestWriteNewNeverOverwritesExisting(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "file.craft")
	if err := WriteNew(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("first"))
	err := WriteNew(p, []byte("second"))
	if !errors.Is(err, fs.ErrExist) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want an already-exists error: %v", err)
	}
	assertContent(t, p, []byte("first"))
	assertNoTemp(t, d)
}

func TestWriteNewCopiesWhereHardLinksFailAndNeverReplaces(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "export.png")
	if err := create(&faulty{noHardLinks: true}, p, PlatformDefault, fillWith("copied")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("copied"))
	assertNoTemp(t, d)

	// A file that appears after the existence check is kept, whether the copy or the hard link
	// finds it.
	for _, noHardLinks := range []bool{true, false} {
		p := filepath.Join(d, fmt.Sprintf("raced-%v.png", noHardLinks))
		f := &faulty{noHardLinks: noHardLinks, createdMeanwhile: original}
		err := create(f, p, PlatformDefault, fillWith("mine"))
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("raced (noHardLinks=%v): %v", noHardLinks, err)
		}
		assertContent(t, p, original)
		if f.renames != 0 {
			t.Fatal("renamed")
		}
	}
	assertNoTemp(t, d)

	// A failed new file has no original to speak of.
	p = filepath.Join(d, "full.png")
	err := create(&faulty{failSync: true}, p, PlatformDefault, fillWith("x"))
	if err == nil || !strings.Contains(err.Error(), "disk is full") || strings.Contains(err.Error(), "original") {
		t.Fatalf("message: %v", err)
	}
	assertMissing(t, p)
	assertNoTemp(t, d)
}

func TestAFailedCopyLeavesNoPartialFile(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "export.png")
	// The temporary file flushes; the copy made in its place does not.
	f := &faulty{noHardLinks: true, syncsBeforeFailure: 1}
	err := opts(f, noRetry).WriteNew(p, []byte("copied"))
	if !errors.Is(err, diskFull) {
		t.Fatalf("want disk full: %v", err)
	}
	if f.syncs != 2 {
		t.Fatalf("syncs = %d", f.syncs)
	}
	assertMissing(t, p)
	assertNoTemp(t, d)
}

func TestRetryDelaysAreCapped(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	f := &faulty{lockedRenames: 2}
	start := time.Now()
	if err := opts(f, RenameRetry{Attempts: 2, FirstDelay: time.Minute}).Write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if f.renames != 3 {
		t.Fatalf("renames = %d", f.renames)
	}
}

func TestDistinguishesAtomicityAndDurabilityOrder(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	writeFile(t, p, original)
	f := &faulty{}
	if err := opts(f, noRetry).Write(p, []byte("durable payload")); err != nil {
		t.Fatal(err)
	}
	syncIdx, renameIdx := slices.Index(f.events, "sync_file"), slices.Index(f.events, "rename")
	if syncIdx < 0 || renameIdx < 0 || syncIdx > renameIdx {
		t.Fatalf("content must be flushed before the rename: %v", f.events)
	}
	// On Unix the directory is flushed after the rename (RealFS makes that a no-op elsewhere,
	// but the call order is the same).
	if dirIdx := slices.Index(f.events, "sync_dir"); dirIdx < renameIdx {
		t.Fatalf("directory must be flushed after the rename: %v", f.events)
	}
}

func TestSyncDirFailureIsBestEffort(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.craft")
	if err := opts(&faulty{failSyncDir: true}, noRetry).Write(p, []byte("best effort data")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("best effort data"))
	assertNoTemp(t, d)
}

// From tests/atomic_tests.rs.

func TestWriteFuncWithStreamingWriter(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.dat")
	writeFile(t, p, original)
	if err := WriteFunc(p, fillWith("chunk1 chunk2")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("chunk1 chunk2"))
	assertNoTemp(t, d)
}

func TestWriteNewFuncWithStreamingWriter(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "new.dat")
	if err := WriteNewFunc(p, fillWith("new content")); err != nil {
		t.Fatal(err)
	}
	assertContent(t, p, []byte("new content"))
	assertNoTemp(t, d)
	if err := WriteNewFunc(p, fillWith("overwritten?")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second write: %v", err)
	}
	assertContent(t, p, []byte("new content"))
}

func TestStreamingFailureAbortsAndKeepsOriginal(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "doc.dat")
	writeFile(t, p, original)
	err := WriteFunc(p, func(f *os.File) error {
		if _, err := f.WriteString("half a file"); err != nil {
			return err
		}
		return fmt.Errorf("disk full: %w", diskFull)
	})
	if !errors.Is(err, diskFull) {
		t.Fatalf("want the callback's error: %v", err)
	}
	assertContent(t, p, original)
	assertNoTemp(t, d)
}

func TestAppleFullFsyncFallbackBehavior(t *testing.T) {
	apple := runtime.GOOS == "darwin" || runtime.GOOS == "ios"
	for _, c := range []struct {
		errno syscall.Errno
		name  string
	}{{45, "ENOTSUP (SMB)"}, {25, "ENOTTY"}} {
		fsynced := false
		err := OrPlainFsync(c.errno, func() error { fsynced = true; return nil })
		if (err == nil) != apple || fsynced != apple {
			t.Errorf("%s: err=%v fsynced=%v, want fallback only on Apple", c.name, err, fsynced)
		}
	}
	// Unrelated errors (EIO = 5) never fall back.
	err := OrPlainFsync(syscall.Errno(5), func() error { t.Fatal("must not run for EIO"); return nil })
	if !errnoIs(err, 5) {
		t.Fatalf("EIO: %v", err)
	}
	if OrPlainFsync(nil, func() error { t.Fatal("must not run on success"); return nil }) != nil {
		t.Fatal("success must stay success")
	}
}

// Go-specific API checks.

func TestTempName(t *testing.T) {
	a, b := TempName("doc.craft"), TempName("doc.craft")
	if a == b || !strings.HasPrefix(a, ".doc.craft.") || !strings.HasSuffix(a, ".craft_tmp") {
		t.Fatalf("TempName: %q, %q", a, b)
	}
	long := strings.Repeat("é", 100)
	if got := TempName(long); !strings.HasPrefix(got, "."+strings.Repeat("é", 64)+".") {
		t.Fatalf("stem must be the first 64 characters: %q", got)
	}
}

func TestNotAFilePath(t *testing.T) {
	d := t.TempDir()
	for _, p := range []string{d + string(os.PathSeparator) + ".", d + string(os.PathSeparator) + "..", string(os.PathSeparator)} {
		if err := Write(p, []byte("x")); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Write(%q) = %v, want fs.ErrInvalid", p, err)
		}
	}
}

func TestSplit(t *testing.T) {
	sep := string(os.PathSeparator)
	for _, c := range [][3]string{
		{"doc.craft", ".", "doc.craft"},
		{"a" + sep + "doc.craft", "a", "doc.craft"},
		{"a" + sep + "b" + sep, "a", "b"},
		{sep + "doc", sep, "doc"},
	} {
		dir, name, err := split(c[0], c[0])
		if err != nil || dir != c[1] || name != c[2] {
			t.Errorf("split(%q) = %q, %q, %v; want %q, %q", c[0], dir, name, err, c[1], c[2])
		}
	}
}
