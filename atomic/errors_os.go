package atomic

import (
	"errors"
	"io/fs"
	"runtime"
	"syscall"
)

// errnoIs reports whether err wraps the system error number n.
func errnoIs(err error, n uintptr) bool {
	var e syscall.Errno
	return errors.As(err, &e) && uintptr(e) == n
}

const windows = runtime.GOOS == "windows"

// Windows error codes the classification needs.
const (
	errorNotSameDevice    = 17  // ERROR_NOT_SAME_DEVICE
	errorSharingViolation = 32  // ERROR_SHARING_VIOLATION
	errorLockViolation    = 33  // ERROR_LOCK_VIOLATION
	errorHandleDiskFull   = 39  // ERROR_HANDLE_DISK_FULL
	errorDiskFull         = 112 // ERROR_DISK_FULL
)

func isReadOnlyFS(err error) bool { return !windows && errors.Is(err, syscall.EROFS) }

func isDiskFull(err error) bool {
	if windows {
		return errnoIs(err, errorDiskFull) || errnoIs(err, errorHandleDiskFull)
	}
	return errors.Is(err, syscall.ENOSPC)
}

func isCrossDevice(err error) bool {
	if windows {
		return errnoIs(err, errorNotSameDevice)
	}
	return errors.Is(err, syscall.EXDEV)
}

// isTransientLock: another process holds the file (a permission error, or on Windows a sharing
// or lock violation).
func isTransientLock(err error) bool {
	return errors.Is(err, fs.ErrPermission) || (windows && (errnoIs(err, errorSharingViolation) || errnoIs(err, errorLockViolation)))
}
