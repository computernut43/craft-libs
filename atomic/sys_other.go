//go:build !unix

package atomic

const unixLike = false

func syncDir(string) error { return nil }

// checkWritable: Windows refuses to rename over a read-only file by itself.
func checkWritable(string) error { return nil }
