package main

import "testing"

func TestIsModule(t *testing.T) {
	for in, want := range map[string]bool{
		"module " + module + "\n\ngo 1.26.0\n":       true,
		"module " + module + "\r\n\r\ngo 1.26.0\r\n": true, // CRLF checkout on Windows
		"module " + module:                           true,
		"module " + module + "x\n":                   false,
		"module example.com/other\n":                 false,
		"":                                           false,
	} {
		if got := isModule([]byte(in)); got != want {
			t.Errorf("isModule(%q) = %v, want %v", in, got, want)
		}
	}
}
