// Command ci runs craft-libs' Go quality gates: `go run ./tools/ci [gate…]`, where a gate is one
// of fmt, vet, staticcheck, test, nopanic, wasm, licences, vuln, or all (the default).
//
// GOFLAGS=-mod=mod is set for every step: the Rust workspace keeps its third-party sources in a
// top-level vendor/ directory, which the go command would otherwise take for Go vendoring.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/computernut43/craft-libs/devtools/gates"
)

const module = "github.com/computernut43/craft-libs"

// order is the order `all` runs the gates in: cheap checks first.
var order = []string{"fmt", "vet", "nopanic", "staticcheck", "test", "wasm", "licences", "vuln"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ci:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	c := gates.Config{
		Dir:      root,
		Env:      []string{"GOFLAGS=-mod=mod"},
		SkipDirs: []string{"vendor", "target", "testdata"},
	}
	steps := map[string]func() error{
		"fmt":         c.Gofmt,
		"vet":         c.Vet,
		"nopanic":     func() error { return c.NoPanic(module + "/devtools/nopanic/cmd/nopanic") },
		"staticcheck": c.Staticcheck,
		"test":        c.Test,
		"wasm": func() error {
			// Libraries must build for the browser; the development tools needn't.
			pkgs, err := c.Packages(module+"/devtools", module+"/tools")
			if err != nil {
				return err
			}
			return c.Wasm(pkgs)
		},
		"licences": c.Licences,
		"vuln":     c.Vuln,
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "all") {
		args = order
	}
	for _, name := range args {
		step, ok := steps[name]
		if !ok {
			return fmt.Errorf("unknown gate %q (want one of %v or all)", name, order)
		}
		fmt.Printf("\n=== ci: %s ===\n", name)
		err := step()
		switch {
		case errors.Is(err, gates.ErrNothingToCheck):
			fmt.Printf("ci: %s: skipped (%v)\n", name, err)
		case err != nil:
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	fmt.Printf("\nci: all %d gate(s) passed\n", len(args))
	return nil
}

// moduleRoot finds the directory holding this module's go.mod, from the working directory up.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && isModule(data) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run inside the craft-libs module (no go.mod for " + module + " found)")
		}
		dir = parent
	}
}

func isModule(gomod []byte) bool {
	const want = "module " + module + "\n"
	return len(gomod) >= len(want) && string(gomod[:len(want)]) == want
}
