package xshelley_test

import (
	"context"
	"fmt"
	"log"

	"exe.dev/xshelley"
)

func Example() {
	ctx := context.Background()

	// Get the shelley binary for amd64 architecture (always Linux)
	path, err := xshelley.GetShelley(ctx, "amd64")
	if err != nil {
		log.Fatalf("failed to get shelley binary: %v", err)
	}

	fmt.Printf("Shelley binary available at: %s\n", path)
	// Now you can use the shelley binary at 'path'
}
