package a

import "os"

// Tests may crash on purpose: nothing here is reported.
func helper() {
	panic("fine in tests")
	os.Exit(2)
}
