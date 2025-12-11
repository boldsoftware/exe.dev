package completeinit_test

import (
	"testing"

	"exe.dev/completeinit"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, completeinit.Analyzer, "a")
}

func TestAnalyzerCrossPackage(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, completeinit.Analyzer, "a", "b")
}
