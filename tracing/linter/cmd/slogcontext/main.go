package main

import (
	"exe.dev/tracing/linter"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(linter.Analyzer)
}
