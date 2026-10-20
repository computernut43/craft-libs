//go:build unix

package atomic

import "os"

// unixLike: modes are kept across replacements, and directories are flushed.
const unixLike = true

// syncDir flushes a directory entry change to stable storage.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = SyncFile(d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkWritable refuses to replace a file the user can't write, as writing it in place would
// (a rename only needs the folder to be writable). Opening for writing without truncating
// changes nothing.
func checkWritable(target string) error {
	f, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}
