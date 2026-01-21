// genregiondocs generates markdown documentation for regions.
//
// Usage:
//
//	go run ./cmd/genregiondocs
//
// This outputs a markdown file to docs/content/regions.md.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"exe.dev/region"
)

const outputDir = "docs/content"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "genregiondocs: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Find git root to ensure we write to the correct location
	root, err := findGitRoot()
	if err != nil {
		return err
	}
	outDir := filepath.Join(root, outputDir)

	doc := generateRegionDoc()
	outPath := filepath.Join(outDir, "regions.md")
	if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	return nil
}

func generateRegionDoc() string {
	var buf bytes.Buffer

	// Front matter
	buf.WriteString(`---
# DO NOT EDIT; rebuild with go run ./cmd/genregiondocs
title: "Regions"
description: "Available regions for exe.dev VMs"
subheading: "2. Features"
suborder: 10
published: false
---

# Regions

exe.dev VMs can be deployed in multiple regions around the world.

`)

	// Generate list items for active regions only
	for _, r := range region.All() {
		if !r.Active {
			continue
		}
		fmt.Fprintf(&buf, "- **%s**: %s\n", r.Code, r.Display)
	}

	return buf.String()
}

func findGitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding git root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
