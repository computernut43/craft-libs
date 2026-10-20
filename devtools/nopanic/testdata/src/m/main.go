package main

import "os"

// os.Exit is how a command reports its exit status; allowed in package main.
func main() {
	os.Exit(run())
}

func run() int {
	panic("still not allowed in main") // want `panic in production code`
}
