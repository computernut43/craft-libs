// Command nopanic runs the nopanic analysis pass: `go run ./devtools/nopanic/cmd/nopanic ./...`.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/computernut43/craft-libs/devtools/nopanic"
)

func main() { singlechecker.Main(nopanic.Analyzer) }
