// Package atomic is crash-safe file replacement for the Craft suite: the Go port of the
// craft-atomic Rust crate (crates/atomic).
//
// [Write] never truncates the destination in place. It writes a temporary file in the same
// directory as the destination, flushes it to stable storage, renames it over the destination
// atomically and, on Unix, flushes the directory so the rename survives a power loss. If writing
// or renaming fails, the destination keeps its original bytes and the temporary file is removed.
//
// # Atomicity and durability
//
// Readers never see a partial write: the destination has either its previous bytes or all the new
// ones. An abnormal exit (kill, crash, power loss) can leave a temporary file (".…craft_tmp")
// beside the destination; sweeping those is the application's job. Two sync points order the
// write: the staged content is flushed before the rename (F_FULLFSYNC on Apple platforms, with a
// plain fsync where a file system refuses it), and on Unix the directory is flushed after the
// rename, best effort, because the rename has already committed. Drive write caches can still
// lose data on power loss; software can't prevent that.
//
// # Permissions
//
// New documents and exports get normal creation permissions, also while staging. Use [Options]
// with [Private] for settings and other sensitive data. Replacing an existing file always stages
// privately and keeps the existing Unix mode.
//
// # Platforms
//
// On Windows the rename is retried with exponential backoff while antivirus scanners or indexers
// briefly hold the file ([RenameRetry]). Staging beside the destination keeps the rename on one
// volume. In the browser (js/wasm) the operations return the platform's errors; nothing panics.
// [Options.FS] takes an [FS] so tests can inject disk-full, lock or permission failures at
// each step.
//
// Differences from the Rust crate: error messages end with Go's wording of the underlying error;
// a failure to close the staged file fails the save (Rust ignores it, after a successful flush);
// a destination path ending in "." or ".." is refused as not a file path.
package atomic

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

// NewFilePermissions is the permission policy for files that don't exist yet. Existing
// destinations always keep their mode.
type NewFilePermissions int

const (
	// PlatformDefault gives new files the permissions ordinary creation with mode 0666 would get:
	// the OS applies the umask or default ACL; nothing changes the process umask.
	PlatformDefault NewFilePermissions = iota
	// Private stages and creates new files with mode 0600 (subject to umask/ACL), unless a
	// streaming callback selects another mode. Use it for settings, credentials and recovery data.
	Private
)

func (p NewFilePermissions) String() string {
	if p == Private {
		return "Private"
	}
	return "PlatformDefault"
}

// FS is the file-system operations an atomic write performs. [RealFS] is the production one;
// tests implement it to inject faults.
type FS interface {
	// CreateNew exclusively creates a file for writing; on Unix with mode 0600 for Private or
	// 0666 for PlatformDefault, which the OS narrows by umask/default ACL.
	CreateNew(path string, perm NewFilePermissions) (*os.File, error)
	// Sync flushes a written file to stable storage.
	Sync(f *os.File) error
	// Rename renames from over to, replacing it atomically.
	Rename(from, to string) error
	// HardLink gives the file from the second name to; it fails with fs.ErrExist when to exists.
	HardLink(from, to string) error
	// Remove removes a file.
	Remove(path string) error
	// SyncDir flushes a directory entry change to stable storage (Unix); a no-op elsewhere.
	SyncDir(dir string) error
	// SetPermissions gives the open file mode.
	SetPermissions(f *os.File, mode fs.FileMode) error
}

// RealFS is the production [FS].
type RealFS struct{}

// CreateNew implements [FS].
func (RealFS) CreateNew(path string, perm NewFilePermissions) (*os.File, error) {
	// The policy applies at creation: a later chmod can't revoke an already-open handle to
	// private payload, and ordinary new documents need no chmod at all.
	mode := fs.FileMode(0o666)
	if perm == Private {
		mode = 0o600
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
}

// Sync implements [FS].
func (RealFS) Sync(f *os.File) error { return SyncFile(f) }

// Rename implements [FS].
func (RealFS) Rename(from, to string) error { return os.Rename(from, to) }

// HardLink implements [FS].
func (RealFS) HardLink(from, to string) error { return os.Link(from, to) }

// Remove implements [FS].
func (RealFS) Remove(path string) error { return os.Remove(path) }

// SyncDir implements [FS].
func (RealFS) SyncDir(dir string) error { return syncDir(dir) }

// SetPermissions implements [FS].
func (RealFS) SetPermissions(f *os.File, mode fs.FileMode) error { return f.Chmod(mode) }

// SyncFile flushes f to stable storage. On Apple platforms Go's File.Sync issues F_FULLFSYNC and
// falls back to fsync where it is unsupported (ENOTSUP); this also falls back on ENOTTY, which
// some virtual file systems return.
func SyncFile(f *os.File) error {
	return OrPlainFsync(f.Sync(), func() error { return plainFsync(f) })
}

// OrPlainFsync returns full, or the result of fsync if full failed only because F_FULLFSYNC is
// unsupported (Apple platforms: ENOTSUP or ENOTTY).
func OrPlainFsync(full error, fsync func() error) error {
	if full != nil && fullFlushUnsupported(full) {
		return fsync()
	}
	return full
}

func fullFlushUnsupported(err error) bool {
	const enotty, enotsup = 25, 45 // Apple's numbers
	return (runtime.GOOS == "darwin" || runtime.GOOS == "ios") && (errnoIs(err, enotsup) || errnoIs(err, enotty))
}

// RenameRetry configures retries of a rename that meets a transient file lock ([RetryRename]).
type RenameRetry struct {
	// Attempts is the number of retries after the first attempt (0 = no retry).
	Attempts int
	// FirstDelay is the pause before the first retry; it doubles on each retry, up to 200 ms.
	FirstDelay time.Duration
}

// PlatformRetry is the default: 7 retries from 10 ms on Windows, none elsewhere.
func PlatformRetry() RenameRetry {
	if runtime.GOOS == "windows" {
		return RenameRetry{Attempts: 7, FirstDelay: 10 * time.Millisecond}
	}
	return RenameRetry{}
}

// maxRetryDelay is the longest pause between two rename attempts.
const maxRetryDelay = 200 * time.Millisecond

// RetryRename runs rename, retrying with exponential backoff while it fails because another
// process holds the file: a permission error, or on Windows a sharing or lock violation.
func RetryRename(retry RenameRetry, rename func() error) error {
	delay := min(max(retry.FirstDelay, 0), maxRetryDelay)
	left := retry.Attempts
	for {
		err := rename()
		if err == nil || !isTransientLock(err) || left <= 0 {
			return err
		}
		left--
		if delay > 0 {
			time.Sleep(delay)
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

// Options holds the policy for a write. The zero value is the default: normal new-file
// permissions, the real file system and the platform's rename retries.
type Options struct {
	// NewFilePermissions applies to new files; existing Unix files keep their mode.
	NewFilePermissions NewFilePermissions
	// FS performs the file operations; nil means [RealFS].
	FS FS
	// Retry overrides [PlatformRetry] for the rename when not nil.
	Retry *RenameRetry
}

func (o Options) fs() FS {
	if o.FS != nil {
		return o.FS
	}
	return RealFS{}
}

func (o Options) retry() RenameRetry {
	if o.Retry != nil {
		return *o.Retry
	}
	return PlatformRetry()
}

// Write atomically replaces path's content with data (creating it if needed).
func (o Options) Write(path string, data []byte) error {
	return o.WriteFunc(path, writeAll(data))
}

// WriteFunc atomically replaces path's content with what fill writes to the staged file. On
// Unix, a new destination is staged as the policy says; a replacement is staged privately (0600)
// and given the existing mode after fill. For new files, a mode fill selects is kept.
func (o Options) WriteFunc(path string, fill func(*os.File) error) error {
	return replace(o.fs(), o.retry(), path, o.NewFilePermissions, fill)
}

// WriteNew exclusively creates path with data; it never replaces an existing file.
func (o Options) WriteNew(path string, data []byte) error {
	return o.WriteNewFunc(path, writeAll(data))
}

// WriteNewFunc exclusively creates path with what fill writes. A mode fill selects is kept.
func (o Options) WriteNewFunc(path string, fill func(*os.File) error) error {
	return create(o.fs(), path, o.NewFilePermissions, fill)
}

// Write atomically replaces path's content with data, with the default [Options].
func Write(path string, data []byte) error { return Options{}.Write(path, data) }

// WriteFunc atomically replaces path's content with what fill writes, with the default [Options].
func WriteFunc(path string, fill func(*os.File) error) error { return Options{}.WriteFunc(path, fill) }

// WriteNew exclusively creates path with data, with the default [Options].
func WriteNew(path string, data []byte) error { return Options{}.WriteNew(path, data) }

// WriteNewFunc exclusively creates path with what fill writes, with the default [Options].
func WriteNewFunc(path string, fill func(*os.File) error) error {
	return Options{}.WriteNewFunc(path, fill)
}

func writeAll(data []byte) func(*os.File) error {
	return func(f *os.File) error {
		_, err := f.Write(data)
		return err
	}
}

// permBits are the mode bits a replacement keeps from the file it replaces.
const permBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// replace stages the new content beside the target, then renames it over the target.
func replace(fsys FS, retry RenameRetry, path string, newPerm NewFilePermissions, fill func(*os.File) error) error {
	target, err := resolveLinks(path)
	if err != nil {
		return err
	}
	dir, name, err := split(target, path)
	if err != nil {
		return err
	}
	info, statErr := os.Stat(target)
	existed := statErr == nil
	if existed && info.Mode().IsRegular() {
		if err := checkWritable(target); err != nil {
			return readOnlyError(err, path)
		}
	}
	var keep *fs.FileMode
	if existed && unixLike {
		// Windows files carry only a read-only flag, and a read-only file can't be replaced.
		m := info.Mode() & permBits
		keep = &m
	}
	tmp, err := stage(fsys, dir, name, path, existed, keep, newPerm, fill)
	if err != nil {
		return err
	}
	if err := RetryRename(retry, func() error { return fsys.Rename(tmp, target) }); err != nil {
		_ = fsys.Remove(tmp)
		return renameError(err, path, existed)
	}
	// The new bytes are in place; a failed directory flush can't be undone and must not report
	// the save as failed.
	_ = fsys.SyncDir(dir)
	return nil
}

// stage creates a temporary file in dir, fills it, gives it the replaced file's mode (keep, Unix)
// and flushes it. A leftover file with the same name is skipped. When any step fails, the
// temporary file is removed.
func stage(fsys FS, dir, name, path string, existed bool, keep *fs.FileMode, newPerm NewFilePermissions, fill func(*os.File) error) (string, error) {
	// New files follow the caller's policy; existing destinations always stage privately, even
	// when their final mode is shared.
	creation := newPerm
	if existed {
		creation = Private
	}
	for range 8 {
		tmp := filepath.Join(dir, TempName(name))
		f, err := fsys.CreateNew(tmp, creation)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", writeError(err, path, dir, existed)
		}
		werr := fill(f)
		if werr != nil {
			werr = writeError(werr, path, dir, existed)
		} else if keep != nil {
			if err := fsys.SetPermissions(f, *keep); err != nil {
				werr = permissionsError(err, path)
			}
		}
		if werr == nil {
			if err := fsys.Sync(f); err != nil {
				werr = writeError(err, path, dir, existed)
			}
		}
		if err := f.Close(); werr == nil && err != nil {
			werr = writeError(err, path, dir, existed)
		}
		if werr != nil {
			_ = fsys.Remove(tmp)
			return "", werr
		}
		return tmp, nil
	}
	return "", &Error{msg: dir + ": could not create a temporary file", err: fs.ErrExist}
}

// create stages the content beside the target, then gives it the target's name with a hard link,
// which fails rather than replace a file that appeared in the meantime. Volumes without hard
// links get a copy into a newly created file.
func create(fsys FS, path string, newPerm NewFilePermissions, fill func(*os.File) error) error {
	target, err := resolveLinks(path)
	if err != nil {
		return err
	}
	dir, name, err := split(target, path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return writeError(fs.ErrExist, path, dir, true)
	}
	tmp, err := stage(fsys, dir, name, path, false, nil, newPerm, fill)
	if err != nil {
		return err
	}
	placed := fsys.HardLink(tmp, target)
	if placed != nil && !errors.Is(placed, fs.ErrExist) {
		placed = copyNew(fsys, tmp, target, newPerm)
	}
	_ = fsys.Remove(tmp)
	if placed != nil {
		// A file that appeared in the meantime is someone else's and stays as it is.
		return writeError(placed, path, dir, errors.Is(placed, fs.ErrExist))
	}
	// The new file is in place; a failed directory flush can't be undone and is best effort.
	_ = fsys.SyncDir(dir)
	return nil
}

// copyNew copies tmp into target, which must not exist yet, and flushes it. A partly written
// target is removed.
func copyNew(fsys FS, tmp, target string, perm NewFilePermissions) error {
	to, err := fsys.CreateNew(target, perm)
	if err != nil {
		return err
	}
	err = func() error {
		from, err := os.Open(tmp)
		if err != nil {
			return err
		}
		defer from.Close()
		var final *fs.FileMode
		if unixLike {
			src, err := from.Stat()
			if err != nil {
				return err
			}
			cur, err := to.Stat()
			if err != nil {
				return err
			}
			if m := src.Mode() & permBits; m != cur.Mode()&permBits {
				final = &m
			}
		}
		// Normal creation already has the right mode/ACL; avoid a chmod volumes may reject. If
		// the callback narrowed the stage, narrow the destination before copying any payload.
		if perm == PlatformDefault && final != nil {
			if err := fsys.SetPermissions(to, *final); err != nil {
				return err
			}
		}
		if _, err := io.Copy(to, from); err != nil {
			return err
		}
		// Private copies stay private until written, then keep a mode the callback selected.
		if perm == Private && final != nil {
			if err := fsys.SetPermissions(to, *final); err != nil {
				return err
			}
		}
		return fsys.Sync(to)
	}()
	if cerr := to.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = fsys.Remove(target)
	}
	return err
}

// maxLinks is how many symbolic links are followed before giving up (a loop, or an absurd chain).
const maxLinks = 40

// resolveLinks follows a chain of symbolic links to the file it ends at (path itself when it is
// not a link), so the link is kept and the file it names is written. A dangling link resolves to
// the missing file it names, which is then created.
func resolveLinks(path string) (string, error) {
	p := path
	for range maxLinks {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return p, nil
		}
		to, err := os.Readlink(p)
		if err != nil {
			return "", &Error{msg: fmt.Sprintf("cannot save %s: %v", path, err), err: err}
		}
		if !filepath.IsAbs(to) {
			if dir := parentDir(p); dir != "" {
				to = joinRaw(dir, to)
			}
		}
		p = to
	}
	return "", &Error{msg: fmt.Sprintf("cannot save %s: too many levels of symbolic links", path), err: fs.ErrInvalid}
}

// parentDir is p without its last element, without cleaning (like Rust's Path::parent): ""
// for a bare name.
func parentDir(p string) string {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	for len(rest) > 1 && os.IsPathSeparator(rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}
	i := len(rest) - 1
	for i >= 0 && !os.IsPathSeparator(rest[i]) {
		i--
	}
	switch {
	case i < 0:
		return vol
	case i == 0:
		return vol + rest[:1]
	}
	return vol + rest[:i]
}

// joinRaw joins without cleaning, so ".." in a link target is left to the OS as in Rust.
func joinRaw(dir, name string) string {
	if os.IsPathSeparator(dir[len(dir)-1]) {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

// split returns the folder target is in and its file name; path is the caller's, for errors.
func split(target, path string) (string, string, error) {
	t := target
	vol := filepath.VolumeName(t)
	for len(t) > len(vol)+1 && os.IsPathSeparator(t[len(t)-1]) {
		t = t[:len(t)-1]
	}
	name := filepath.Base(t)
	if t == vol || name == "." || name == ".." || (len(name) == 1 && os.IsPathSeparator(name[0])) {
		return "", "", &Error{msg: path + ": not a file path", err: fs.ErrInvalid}
	}
	dir := parentDir(t)
	if dir == "" {
		dir = "."
	}
	return dir, name, nil
}

var tempCounter atomic.Uint64

// TempName generates a unique, hidden temporary file name for staging beside file.
func TempName(file string) string {
	n := tempCounter.Add(1) - 1
	stem, count := file, 0
	for i := range file {
		if count == 64 {
			stem = file[:i] // the first 64 characters
			break
		}
		count++
	}
	return fmt.Sprintf(".%s.%d-%d.craft_tmp", stem, os.Getpid(), n)
}

// Error is a failed save: a message for people, wrapping the underlying error, so errors.Is
// (fs.ErrExist, fs.ErrNotExist, fs.ErrPermission, syscall.ENOSPC, …) still works.
type Error struct {
	msg string
	err error
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Unwrap() error { return e.err }

// unchanged is the end of a message: whether an original file was left as it was.
func unchanged(existed bool) string {
	if existed {
		return "; the original file was not changed"
	}
	return ""
}

func writeError(err error, path, dir string, existed bool) error {
	kept := unchanged(existed)
	var msg string
	switch {
	case errors.Is(err, fs.ErrExist):
		msg = fmt.Sprintf("cannot save %s: the file already exists%s", path, kept)
	case errors.Is(err, fs.ErrNotExist):
		msg = fmt.Sprintf("cannot save %s: the folder %s does not exist (%v)%s", path, dir, err, kept)
	case errors.Is(err, fs.ErrPermission) || isReadOnlyFS(err):
		msg = fmt.Sprintf("cannot save %s: the folder %s is not writable (%v)%s", path, dir, err, kept)
	case isDiskFull(err):
		msg = fmt.Sprintf("cannot save %s: the disk is full (%v)%s", path, err, kept)
	default:
		msg = fmt.Sprintf("cannot save %s: %v%s", path, err, kept)
	}
	return &Error{msg: msg, err: err}
}

func permissionsError(err error, path string) error {
	return &Error{msg: fmt.Sprintf("cannot save %s: the new file cannot be given the original's permissions (%v)%s", path, err, unchanged(true)), err: err}
}

func readOnlyError(err error, path string) error {
	kept := unchanged(true)
	if errors.Is(err, fs.ErrPermission) || isReadOnlyFS(err) {
		return &Error{msg: fmt.Sprintf("cannot save %s: the file is read-only (%v)%s", path, err, kept), err: err}
	}
	return &Error{msg: fmt.Sprintf("cannot save %s: %v%s", path, err, kept), err: err}
}

func renameError(err error, path string, existed bool) error {
	kept := unchanged(existed)
	var msg string
	switch {
	case isCrossDevice(err):
		msg = fmt.Sprintf("cannot save %s: the temporary file is on a different volume than the destination (%v)%s", path, err, kept)
	case errors.Is(err, fs.ErrPermission):
		msg = fmt.Sprintf("cannot save %s: the file is locked or read-only (%v)%s", path, err, kept)
	default:
		msg = fmt.Sprintf("cannot save %s: %v%s", path, err, kept)
	}
	return &Error{msg: msg, err: err}
}
