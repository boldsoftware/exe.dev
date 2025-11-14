package linter_test

import (
	"testing"

	"exe.dev/tracing/linter"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, linter.Analyzer, "a")
}
