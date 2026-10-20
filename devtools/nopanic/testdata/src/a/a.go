package a

import (
	"errors"
	"log"
	"os"
	"regexp"
	"text/template"
)

const pattern = "a+b"

var (
	okConst   = regexp.MustCompile("a+")
	okNamed   = regexp.MustCompile(pattern)
	okConcat  = regexp.MustCompile(pattern + "c")
	badDyn    = regexp.MustCompile(os.Getenv("P"))              // want `MustCompile panics on failure`
	badTmpl   = template.Must(template.New("x").Parse("{{.}}")) // want `Must panics on failure`
	errSample = errors.New("x")
)

type thing struct{}

func (thing) MustGet(key string) int { return len(key) }

func Bad(s string) {
	panic("boom")       // want `panic in production code`
	(panic)("paren")    // want `panic in production code`
	log.Panicf("%s", s) // want `log.Panicf crashes or exits`
	log.Fatal(s)        // want `log.Fatal crashes or exits`
	l := log.Default()
	l.Fatalln(s)       // want `log.Fatalln crashes or exits`
	os.Exit(1)         // want `os.Exit outside package main`
	thing{}.MustGet(s) // want `MustGet panics on failure`
}

func Good(s string) error {
	_ = thing{}.MustGet("constant")
	log.Printf("%s", s)
	if s == "" {
		return errSample
	}
	return nil
}

func Generic[T any](v T) T { return v }

func UsesGeneric() { _ = Generic[int](1) }
