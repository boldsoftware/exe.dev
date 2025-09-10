package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
)

//go:embed prompt.txt
var promptTemplate string

// SystemPromptData contains all the data needed to render the system prompt template
type SystemPromptData struct {
	WorkingDirectory string
	GitInfo          *GitInfo
}

type GitInfo struct {
	Root string
}

// GenerateSystemPrompt generates the system prompt using the embedded template
func GenerateSystemPrompt() (string, error) {
	data, err := collectSystemData()
	if err != nil {
		return "", fmt.Errorf("failed to collect system data: %w", err)
	}

	tmpl, err := template.New("system_prompt").Parse(promptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

func collectSystemData() (*SystemPromptData, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	data := &SystemPromptData{
		WorkingDirectory: wd,
	}

	// Try to collect git info
	gitInfo, err := collectGitInfo()
	if err == nil {
		data.GitInfo = gitInfo
	}

	return data, nil
}

func collectGitInfo() (*GitInfo, error) {
	// Find git root
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	rootOutput, err := rootCmd.Output()
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(string(rootOutput))

	return &GitInfo{
		Root: root,
	}, nil
}
