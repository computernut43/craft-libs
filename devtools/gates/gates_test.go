package gates

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	mitText    = "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy"
	apacheText = "                                 Apache License\n                           Version 2.0, January 2004"
	bsd3Text   = "Redistribution and use in source and binary forms, with or without modification...\n   * Neither the name of Google LLC nor the names of its contributors"
	bsd2Text   = "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:"
	gplText    = "                    GNU GENERAL PUBLIC LICENSE\n                       Version 3, 29 June 2007"
	lgplText   = "                   GNU LESSER GENERAL PUBLIC LICENSE\n                       Version 3, 29 June 2007"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"mit", mitText, []string{"MIT"}},
		{"apache", apacheText, []string{"Apache-2.0"}},
		{"bsd3", bsd3Text, []string{"BSD-3-Clause"}},
		{"bsd2", bsd2Text, []string{"BSD-2-Clause"}},
		{"isc", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.", []string{"ISC"}},
		{"0bsd", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted.", []string{"0BSD"}},
		{"unlicense", "This is free and unencumbered software released into the public domain.", []string{"Unlicense"}},
		{"zlib", "This software is provided 'as-is'... Permission is granted to anyone to use this software for any purpose... 2. Altered source versions must be plainly marked as such", []string{"Zlib"}},
		{"gpl", gplText, []string{"GPL"}},
		{"lgpl wins over gpl", lgplText + "\nGNU GENERAL PUBLIC LICENSE", []string{"LGPL"}},
		{"agpl", "GNU AFFERO GENERAL PUBLIC LICENSE", []string{"AGPL"}},
		{"mpl", "Mozilla Public License Version 2.0", []string{"MPL-2.0"}},
		{"spdx dual", "This project is provided under the terms of the UNLICENSE or the BSD license denoted by the following SPDX identifier:\n\nSPDX-License-Identifier: Unlicense OR BSD-3-Clause\n", []string{"BSD-3-Clause", "Unlicense"}},
		{"spdx wins over text", "SPDX-License-Identifier: MIT\n" + gplText, []string{"MIT"}},
		{"apache and mit in one file", "The MCP project is undergoing a licensing transition...\n" + apacheText + "\n" + mitText, []string{"Apache-2.0", "MIT"}},
		{"unknown", "All rights reserved.", []string{}},
		{"empty", "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.text); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Classify = %q, want %q", got, c.want)
			}
		})
	}
}

func TestProblem(t *testing.T) {
	cases := []struct {
		lic  []string
		want string
	}{
		{[]string{"MIT"}, ""},
		{[]string{"BSD-3-Clause", "Unlicense"}, ""},
		{nil, "no licence file recognised"},
		{[]string{"GPL"}, "licence not allowed: GPL"},
		{[]string{"MIT", "LGPL"}, "licence not allowed: LGPL"},
		{[]string{"MPL-2.0"}, "licence not allowed: MPL-2.0"},
	}
	for _, c := range cases {
		if got := (Module{Licences: c.lic}).Problem(); got != c.want {
			t.Errorf("Problem(%q) = %q, want %q", c.lic, got, c.want)
		}
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModuleLicences(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "LICENSE-MIT"), mitText)
	write(t, filepath.Join(dir, "LICENCE_APACHE.txt"), apacheText)
	write(t, filepath.Join(dir, "README.md"), gplText) // not a licence file name: ignored
	write(t, filepath.Join(dir, "licenses", "x"), gplText)
	got, err := ModuleLicences(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Apache-2.0", "MIT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ModuleLicences = %q, want %q", got, want)
	}
	if _, err := ModuleLicences(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing directory: want an error")
	}
}

func TestGoFilesSkipsHiddenAndSkipDirs(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a.go", "p/b.go", "vendor/krilla/c.go", ".git/d.go", "target/e.go", "p/readme.md"} {
		write(t, filepath.Join(dir, p), "package x\n")
	}
	got, err := Config{Dir: dir, SkipDirs: []string{"vendor", "target"}}.GoFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "p", "b.go")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GoFiles = %q, want %q", got, want)
	}
}

func TestGofmt(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ok.go"), "package x\n\nfunc F() {}\n")
	var out bytes.Buffer
	c := Config{Dir: dir, Stdout: &out, Stderr: &out}
	if err := c.Gofmt(); err != nil {
		t.Fatalf("formatted tree: %v", err)
	}
	write(t, filepath.Join(dir, "bad.go"), "package x\nfunc  G( ) {  }\n")
	err := c.Gofmt()
	if err == nil || !strings.Contains(err.Error(), "bad.go") {
		t.Fatalf("unformatted file: err = %v, want it named", err)
	}
	if err := (Config{Dir: t.TempDir()}).Gofmt(); err != nil {
		t.Fatalf("no Go files: %v", err)
	}
}

func TestRunReportsFailureAndPassesEnv(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	c := Config{Dir: dir, Env: []string{"CRAFT_GATES_TEST=from-config"}, Stdout: &out, Stderr: &out}
	got, err := c.Output([]string{"CRAFT_GATES_EXTRA=from-call"}, "go", "env", "GOOS")
	if err != nil || strings.TrimSpace(string(got)) == "" {
		t.Fatalf("go env GOOS: %q, %v", got, err)
	}
	if err := c.Run(nil, "go", "no-such-subcommand"); err == nil {
		t.Fatal("failing command: want an error")
	}
	if err := c.Run(nil, filepath.Join(dir, "no-such-binary")); err == nil {
		t.Fatal("missing binary: want an error")
	}
}

func TestWasmNeedsPackages(t *testing.T) {
	if err := (Config{Dir: t.TempDir()}).Wasm(nil); err != ErrNothingToCheck {
		t.Fatalf("Wasm(nil) = %v, want ErrNothingToCheck", err)
	}
}
