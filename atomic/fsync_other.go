//go:build !darwin

package atomic

import "os"

// plainFsync is only reached on Apple platforms; elsewhere File.Sync is already the plain flush.
func plainFsync(f *os.File) error { return f.Sync() }
