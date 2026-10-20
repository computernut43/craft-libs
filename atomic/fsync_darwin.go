package atomic

import (
	"os"
	"syscall"
)

// plainFsync is fsync(2), for file systems that refuse F_FULLFSYNC.
func plainFsync(f *os.File) error { return syscall.Fsync(int(f.Fd())) }
