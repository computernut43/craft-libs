package gates

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Allowed are the licences dependencies may carry (craftrules licensing standard: permissive
// open-source licences only; nothing GPL, LGPL, AGPL or MPL is linked).
var Allowed = map[string]bool{
	"MIT": true, "Apache-2.0": true, "BSD-2-Clause": true, "BSD-3-Clause": true,
	"ISC": true, "0BSD": true, "Unlicense": true, "Zlib": true,
}

// Module is one dependency module and the licences found in its root directory.
type Module struct {
	Path, Version, Dir string
	Licences           []string
}

// Problem says why a module failed the licence gate, or "" when it passed.
func (m Module) Problem() string {
	if len(m.Licences) == 0 {
		return "no licence file recognised"
	}
	// Every licence found must be allowed: a module offering "MIT OR GPL" is conservatively
	// refused too, until someone checks it and the gate learns the exception.
	for _, l := range m.Licences {
		if !Allowed[l] {
			return "licence not allowed: " + l
		}
	}
	return ""
}

// maxLicenceBytes caps how much of one licence file is read.
const maxLicenceBytes = 1 << 20

var (
	licenceName = regexp.MustCompile(`(?i)^(un)?licen[cs]e|^copying`)
	spdxLine    = regexp.MustCompile(`SPDX-License-Identifier:\s*([^\r\n]+)`)
	spdxToken   = regexp.MustCompile(`[A-Za-z0-9.+-]+`)
)

// Classify names the licences in one licence file's text. SPDX identifiers win; otherwise
// well-known licence wording is recognised. Unknown text yields nothing.
func Classify(text string) []string {
	set := map[string]bool{}
	for _, m := range spdxLine.FindAllStringSubmatch(text, -1) {
		for _, tok := range spdxToken.FindAllString(m[1], -1) {
			switch strings.ToUpper(tok) {
			case "OR", "AND", "WITH":
				continue
			}
			set[tok] = true
		}
	}
	if len(set) == 0 {
		has := func(s string) bool { return strings.Contains(text, s) }
		lower := strings.ToLower(text)
		switch {
		case has("GNU AFFERO GENERAL PUBLIC LICENSE"):
			set["AGPL"] = true
		case has("GNU LESSER GENERAL PUBLIC LICENSE") || has("GNU LIBRARY GENERAL PUBLIC LICENSE"):
			set["LGPL"] = true
		case has("GNU GENERAL PUBLIC LICENSE"):
			set["GPL"] = true
		}
		if has("Mozilla Public License") {
			set["MPL-2.0"] = true
		}
		if has("Permission is hereby granted, free of charge") {
			set["MIT"] = true
		}
		if has("Apache License") && has("Version 2.0") {
			set["Apache-2.0"] = true
		}
		if has("Redistribution and use in source and binary forms") {
			if has("Neither the name") || strings.Contains(lower, "names of its contributors") {
				set["BSD-3-Clause"] = true
			} else {
				set["BSD-2-Clause"] = true
			}
		}
		if has("Permission to use, copy, modify, and/or distribute this software for any purpose") {
			if has("provided that the above copyright notice and this permission notice appear in all copies") {
				set["ISC"] = true
			} else {
				set["0BSD"] = true
			}
		}
		if has("This is free and unencumbered software released into the public domain") {
			set["Unlicense"] = true
		}
		if has("Permission is granted to anyone to use this software for any purpose") && strings.Contains(lower, "altered source versions must be plainly marked") {
			set["Zlib"] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// ModuleLicences classifies every licence file in dir's top level.
func ModuleLicences(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !licenceName.MatchString(e.Name()) {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(io.LimitReader(f, maxLicenceBytes))
		if cerr := f.Close(); rerr == nil {
			rerr = cerr
		}
		if rerr != nil {
			return nil, rerr
		}
		for _, l := range Classify(string(data)) {
			set[l] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out, nil
}

// Dependencies lists the non-main modules that provide packages to the module's packages or
// their tests, with their licences.
func (c Config) Dependencies() ([]Module, error) {
	if err := c.Run(nil, "go", "mod", "download"); err != nil {
		return nil, err
	}
	const format = `{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}{{end}}`
	out, err := c.Output(nil, "go", "list", "-deps", "-test", "-f", format, "./...")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var mods []Module
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		m := Module{Path: f[0], Version: f[1], Dir: f[2]}
		lic, err := ModuleLicences(m.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Path, err)
		}
		m.Licences = lic
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// Licences prints every dependency's licences and fails if any is unknown or not allowed.
func (c Config) Licences() error {
	mods, err := c.Dependencies()
	if err != nil {
		return err
	}
	var bad []string
	for _, m := range mods {
		status := "ok"
		if p := m.Problem(); p != "" {
			status = p
			bad = append(bad, m.Path+": "+p)
		}
		fmt.Fprintf(c.out(), "%-50s %-12s %-30s %s\n", m.Path, m.Version, strings.Join(m.Licences, " OR "), status)
	}
	if len(bad) > 0 {
		return fmt.Errorf("dependency licences:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}
