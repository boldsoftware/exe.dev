// gencmddocs generates markdown documentation for SSH CLI commands.
//
// Usage:
//
//	go run ./cmd/gencmddocs
//
// This outputs one markdown file per command into docs/content/cli-<cmd>.md.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"exe.dev/execore"
	"exe.dev/exemenu"
)

const outputDir = "docs/content"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gencmddocs: %v\n", err)
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

	// Create command tree using the real definitions
	ss := &execore.SSHServer{}
	ct := execore.NewCommandTree(ss)

	// Generate one file per command
	var suborder int
	for _, cmd := range ct.Commands {
		if cmd.Hidden {
			continue
		}
		suborder++
		doc := generateCommandDoc(cmd, suborder)
		filename := fmt.Sprintf("cli-%s.md", cmd.Name)
		outPath := filepath.Join(outDir, filename)
		if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", outPath, err)
		}
		fmt.Printf("wrote %s\n", outPath)
	}

	return nil
}

func generateCommandDoc(cmd *exemenu.Command, suborder int) string {
	var buf bytes.Buffer

	// Front matter
	fmt.Fprintf(&buf, `---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "%s"
description: "%s"
subheading: "4. CLI Reference"
suborder: %d
---

`, cmd.Name, cmd.Description, suborder)

	// Title
	fmt.Fprintf(&buf, "# %s\n\n", cmd.Name)

	// Description
	buf.WriteString(cmd.Description + "\n\n")

	// Usage
	if cmd.Usage != "" {
		buf.WriteString("## Usage\n\n```\n" + cmd.Usage + "\n```\n\n")
	}

	// Aliases
	if len(cmd.Aliases) > 0 {
		buf.WriteString("## Aliases\n\n" + strings.Join(cmd.Aliases, ", ") + "\n\n")
	}

	// Options
	if cmd.FlagSetFunc != nil {
		fs := cmd.FlagSetFunc()
		var flags []string
		fs.VisitAll(func(f *flag.Flag) {
			if f.Usage == "" || strings.HasPrefix(f.Usage, "[hidden] ") {
				return // Skip hidden flags
			}
			flags = append(flags, fmt.Sprintf("- `--%s`: %s", f.Name, f.Usage))
		})
		if len(flags) > 0 {
			buf.WriteString("## Options\n\n")
			for _, f := range flags {
				buf.WriteString(f + "\n")
			}
			buf.WriteString("\n")
		}
	}

	// Examples
	if len(cmd.Examples) > 0 {
		buf.WriteString("## Examples\n\n```\n")
		for _, ex := range cmd.Examples {
			buf.WriteString(ex + "\n")
		}
		buf.WriteString("```\n\n")
	}

	// Subcommands
	if len(cmd.Subcommands) > 0 {
		buf.WriteString("## Subcommands\n\n")
		for _, sub := range cmd.Subcommands {
			if sub.Hidden {
				continue
			}
			writeSubcommandDoc(&buf, cmd.Name, sub)
		}
	}

	return buf.String()
}

func writeSubcommandDoc(buf *bytes.Buffer, parentName string, sub *exemenu.Command) {
	fullName := parentName + " " + sub.Name

	buf.WriteString("### " + fullName + "\n\n")
	buf.WriteString(sub.Description + "\n\n")

	if sub.Usage != "" {
		buf.WriteString("**Usage:**\n```\n" + sub.Usage + "\n```\n\n")
	}

	if len(sub.Aliases) > 0 {
		buf.WriteString("**Aliases:** " + strings.Join(sub.Aliases, ", ") + "\n\n")
	}

	if sub.FlagSetFunc != nil {
		fs := sub.FlagSetFunc()
		var flags []string
		fs.VisitAll(func(f *flag.Flag) {
			if f.Usage == "" || strings.HasPrefix(f.Usage, "[hidden] ") {
				return
			}
			flags = append(flags, fmt.Sprintf("- `--%s`: %s", f.Name, f.Usage))
		})
		if len(flags) > 0 {
			buf.WriteString("**Options:**\n")
			for _, f := range flags {
				buf.WriteString(f + "\n")
			}
			buf.WriteString("\n")
		}
	}

	if len(sub.Examples) > 0 {
		buf.WriteString("**Examples:**\n```\n")
		for _, ex := range sub.Examples {
			buf.WriteString(ex + "\n")
		}
		buf.WriteString("```\n\n")
	}
}

func findGitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding git root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
