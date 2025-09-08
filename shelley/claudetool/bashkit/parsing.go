package bashkit

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// ExtractCommands parses a bash command and extracts individual command names that are
// candidates for auto-installation.
//
// Returns only simple command names (no paths, no builtins, no variable assignments)
// that could potentially be missing tools that need installation.
//
// Filtering logic:
// - Excludes commands with paths (./script.sh, /usr/bin/tool, ../build.sh)
// - Excludes shell builtins (echo, cd, test, [, etc.)
// - Excludes variable assignments (FOO=bar)
// - Deduplicates repeated command names
//
// Examples:
//
//	"ls -la && echo done" → ["ls"] (echo filtered as builtin)
//	"./deploy.sh && curl api.com" → ["curl"] (./deploy.sh filtered as path)
//	"yamllint config.yaml" → ["yamllint"] (candidate for installation)
func ExtractCommands(command string) ([]string, error) {
	r := strings.NewReader(command)
	parser := syntax.NewParser()
	file, err := parser.Parse(r, "")
	if err != nil {
		return nil, fmt.Errorf("failed to parse bash command: %w", err)
	}

	var commands []string
	seen := make(map[string]bool)

	syntax.Walk(file, func(node syntax.Node) bool {
		callExpr, ok := node.(*syntax.CallExpr)
		if !ok || len(callExpr.Args) == 0 {
			return true
		}
		cmdName := callExpr.Args[0].Lit()
		if cmdName == "" {
			return true
		}
		if strings.Contains(cmdName, "=") {
			// variable assignment
			return true
		}
		if strings.Contains(cmdName, "/") {
			// commands with slashes are user-specified executables/scripts
			return true
		}
		if interp.IsBuiltin(cmdName) {
			return true
		}
		if !seen[cmdName] {
			seen[cmdName] = true
			commands = append(commands, cmdName)
		}
		return true
	})

	return commands, nil
}
