package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"runtime"

	"exe.dev/xshelley"
)

func main() {
	goarch := flag.String("arch", runtime.GOARCH, "Target architecture (e.g., amd64, arm64)")
	flag.Parse()

	ctx := context.Background()

	path, _, err := xshelley.GetShelley(ctx, *goarch)
	if err != nil {
		log.Fatalf("Failed to get shelley binary: %v", err)
	}

	fmt.Println(path)
}
