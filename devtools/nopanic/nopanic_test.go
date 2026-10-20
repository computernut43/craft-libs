package nopanic_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/computernut43/craft-libs/devtools/nopanic"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), nopanic.Analyzer, "a", "m")
}
