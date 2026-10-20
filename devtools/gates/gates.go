// Package gates holds the quality gates shared by the Craft Go repositories: formatting, vet,
// staticcheck, tests, the never-crash analysis, the WebAssembly build, dependency licences and
// known vulnerabilities. Each repository's tools/ci command runs them, adding its own checks
// (WordCraft adds layering and asset attribution).
//
// Gates run the go command as a child process: no shell, an explicit working directory, a
// timeout, and the caller's environment plus the Config's additions.
package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Tool versions run with `go run module@version`, so they are pinned and checked against the
// checksum database without adding them to the module's requirements.
const (
	StaticcheckVersion = "honnef.co/go/tools/cmd/staticcheck@v0.8.2"
	GovulncheckVersion = "golang.org/x/vuln/cmd/govulncheck@v1.9.0"
)

// Config says where a repository's module lives and how to run the go command in it.
type Config struct {
	// Dir is the module root.
	Dir string
	// Env is added to the environment of every child process, e.g. GOFLAGS=-mod=mod while a
	// repository still has a non-Go vendor/ directory.
	Env []string
	// SkipDirs are directory names never searched for Go files (e.g. "vendor", "target").
	SkipDirs []string
	// Timeout bounds each child process; zero means 20 minutes.
	Timeout time.Duration
	// Stdout and Stderr receive the children's output; nil means os.Stdout / os.Stderr.
	Stdout, Stderr io.Writer
}

func (c Config) out() io.Writer {
	if c.Stdout != nil {
		return c.Stdout
	}
	return os.Stdout
}

func (c Config) errOut() io.Writer {
	if c.Stderr != nil {
		return c.Stderr
	}
	return os.Stderr
}

func (c Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 20 * time.Minute
}

// command builds a child process for `name args…` in the module root.
func (c Config) command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = c.Dir
	cmd.Env = append(append(os.Environ(), c.Env...), env...)
	return cmd
}

// Run runs `name args…`, streaming its output, and fails if it fails.
func (c Config) Run(env []string, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()
	cmd := c.command(ctx, env, name, args...)
	cmd.Stdout, cmd.Stderr = c.out(), c.errOut()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Output runs `name args…` and returns its standard output; standard error is streamed.
func (c Config) Output(env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()
	cmd := c.command(ctx, env, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, c.errOut()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return buf.Bytes(), nil
}

// GoFiles lists the module's .go files, skipping hidden directories and Config.SkipDirs.
func (c Config) GoFiles() ([]string, error) {
	skip := map[string]bool{}
	for _, d := range c.SkipDirs {
		skip[d] = true
	}
	var files []string
	err := filepath.WalkDir(c.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != c.Dir && (strings.HasPrefix(d.Name(), ".") || skip[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// Gofmt fails if any Go file is not gofmt-formatted (simplified, as `gofmt -s`).
func (c Config) Gofmt() error {
	files, err := c.GoFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	out, err := c.Output(nil, "gofmt", append([]string{"-s", "-l"}, files...)...)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return fmt.Errorf("not gofmt -s formatted:\n%s", s)
	}
	return nil
}

// Vet runs go vet on every package.
func (c Config) Vet() error { return c.Run(nil, "go", "vet", "./...") }

// Staticcheck runs the pinned staticcheck on every package.
func (c Config) Staticcheck() error { return c.Run(nil, "go", "run", StaticcheckVersion, "./...") }

// Test runs every package's tests.
func (c Config) Test() error { return c.Run(nil, "go", "test", "./...") }

// NoPanic runs the nopanic analysis through the command at cmdPkg (an import path, so a
// dependent repository runs the version of craft-libs it requires).
func (c Config) NoPanic(cmdPkg string) error { return c.Run(nil, "go", "run", cmdPkg, "./...") }

// Vuln runs the pinned govulncheck on every package.
func (c Config) Vuln() error { return c.Run(nil, "go", "run", GovulncheckVersion, "./...") }

// Packages lists the module's packages (`go list ./...`) whose import path doesn't start with
// any of the excluded prefixes.
func (c Config) Packages(exclude ...string) ([]string, error) {
	out, err := c.Output(nil, "go", "list", "./...")
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, p := range strings.Fields(string(out)) {
		keep := true
		for _, e := range exclude {
			if p == e || strings.HasPrefix(p, e+"/") {
				keep = false
				break
			}
		}
		if keep {
			pkgs = append(pkgs, p)
		}
	}
	return pkgs, nil
}

// ErrNothingToCheck reports a gate that had no packages to look at.
var ErrNothingToCheck = errors.New("no packages to check")

// Wasm builds the given packages for the browser (GOOS=js GOARCH=wasm).
func (c Config) Wasm(pkgs []string) error {
	if len(pkgs) == 0 {
		return ErrNothingToCheck
	}
	return c.Run([]string{"GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0"}, "go", append([]string{"build"}, pkgs...)...)
}
