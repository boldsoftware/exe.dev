package reservevms_test

import (
	"testing"

	"exe.dev/reservevms"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, reservevms.Analyzer, "a", "b", "c")
}
