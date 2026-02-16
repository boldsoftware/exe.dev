// Command exelint runs all bespoke exe linters.
// This is faster than serial execution because it re-uses typechecking state.
package main

import (
	"exe.dev/tracing/linter"
	"github.com/boldsoftware/exe.dev/completeinit"
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(linter.Analyzer, completeinit.Analyzer)
}
