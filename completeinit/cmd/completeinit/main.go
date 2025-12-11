package main

import (
	"exe.dev/completeinit"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(completeinit.Analyzer)
}
