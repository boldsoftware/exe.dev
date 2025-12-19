package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitDiffInfo represents a commit or working changes
type GitDiffInfo struct {
	ID         string    `json:"id"`
	Message    string    `json:"message"`
	Author     string    `json:"author"`
	Timestamp  time.Time `json:"timestamp"`
	FilesCount int       `json:"filesCount"`
	Additions  int       `json:"additions"`
	Deletions  int       `json:"deletions"`
}

// GitFileInfo represents a file in a diff
type GitFileInfo struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added, modified, deleted
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// GitFileDiff represents the content of a file diff
type GitFileDiff struct {
	Path       string `json:"path"`
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
}

// getGitRoot returns the git repository root for the given directory
func getGitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// parseDiffStat parses git diff --numstat output
func parseDiffStat(output string) (additions, deletions, filesCount int) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			if parts[0] != "-" {
				add, _ := strconv.Atoi(parts[0])
				additions += add
			}
			if parts[1] != "-" {
				del, _ := strconv.Atoi(parts[1])
				deletions += del
			}
			filesCount++
		}
	}
	return additions, deletions, filesCount
}

// handleGitDiffs returns available diffs (working changes + recent commits)
func (s *Server) handleGitDiffs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	// Validate cwd is a directory
	fi, err := os.Stat(cwd)
	if err != nil || !fi.IsDir() {
		http.Error(w, "invalid cwd", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	var diffs []GitDiffInfo

	// Working changes
	workingStatCmd := exec.Command("git", "diff", "HEAD", "--numstat")
	workingStatCmd.Dir = gitRoot
	workingStatOutput, _ := workingStatCmd.Output()
	workingAdditions, workingDeletions, workingFilesCount := parseDiffStat(string(workingStatOutput))

	diffs = append(diffs, GitDiffInfo{
		ID:         "working",
		Message:    "Working Changes",
		Author:     "",
		Timestamp:  time.Now(),
		FilesCount: workingFilesCount,
		Additions:  workingAdditions,
		Deletions:  workingDeletions,
	})

	// Get commits
	cmd := exec.Command("git", "log", "--oneline", "-20", "--pretty=format:%H%x00%s%x00%an%x00%at")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) < 4 {
				continue
			}

			timestamp, _ := strconv.ParseInt(parts[3], 10, 64)

			// Get diffstat
			statCmd := exec.Command("git", "diff", parts[0]+"^", parts[0], "--numstat")
			statCmd.Dir = gitRoot
			statOutput, _ := statCmd.Output()
			additions, deletions, filesCount := parseDiffStat(string(statOutput))

			diffs = append(diffs, GitDiffInfo{
				ID:         parts[0],
				Message:    parts[1],
				Author:     parts[2],
				Timestamp:  time.Unix(timestamp, 0),
				FilesCount: filesCount,
				Additions:  additions,
				Deletions:  deletions,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"diffs":   diffs,
		"gitRoot": gitRoot,
	})
}

// handleGitDiffFiles returns the files changed in a specific diff
func (s *Server) handleGitDiffFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract diff ID from path: /api/git/diffs/{id}/files
	path := strings.TrimPrefix(r.URL.Path, "/api/git/diffs/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "files" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	diffID := parts[0]

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	var cmd *exec.Cmd
	var statBaseArg string

	if diffID == "working" {
		cmd = exec.Command("git", "diff", "--name-status", "HEAD")
		statBaseArg = "HEAD"
	} else {
		cmd = exec.Command("git", "diff", "--name-status", diffID+"^")
		statBaseArg = diffID + "^"
	}
	cmd.Dir = gitRoot

	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "failed to get diff files", http.StatusInternalServerError)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var files []GitFileInfo

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		status := "modified"
		switch parts[0] {
		case "A":
			status = "added"
		case "D":
			status = "deleted"
		case "M":
			status = "modified"
		}

		// Get additions/deletions for this file
		statCmd := exec.Command("git", "diff", statBaseArg, "--numstat", "--", parts[1])
		statCmd.Dir = gitRoot
		statOutput, _ := statCmd.Output()
		additions, deletions := 0, 0
		if statOutput != nil {
			statParts := strings.Fields(string(statOutput))
			if len(statParts) >= 2 {
				additions, _ = strconv.Atoi(statParts[0])
				deletions, _ = strconv.Atoi(statParts[1])
			}
		}

		files = append(files, GitFileInfo{
			Path:      parts[1],
			Status:    status,
			Additions: additions,
			Deletions: deletions,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// handleGitFileDiff returns the old and new content for a file
func (s *Server) handleGitFileDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract diff ID and file path from: /api/git/file-diff/{id}/*filepath
	path := strings.TrimPrefix(r.URL.Path, "/api/git/file-diff/")
	slashIdx := strings.Index(path, "/")
	if slashIdx < 0 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	diffID := path[:slashIdx]
	filePath := path[slashIdx+1:]

	if diffID == "" || filePath == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		http.Error(w, "cwd parameter required", http.StatusBadRequest)
		return
	}

	gitRoot, err := getGitRoot(cwd)
	if err != nil {
		http.Error(w, "not a git repository", http.StatusBadRequest)
		return
	}

	// Prevent path traversal
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}

	var oldCmd *exec.Cmd
	if diffID == "working" {
		oldCmd = exec.Command("git", "show", "HEAD:"+filePath)
	} else {
		oldCmd = exec.Command("git", "show", diffID+"^:"+filePath)
	}
	oldCmd.Dir = gitRoot

	oldOutput, _ := oldCmd.Output()
	oldContent := string(oldOutput)

	// Get new version from working tree
	newContent := ""
	fullPath := filepath.Join(gitRoot, cleanPath)
	if file, err := os.Open(fullPath); err == nil {
		if fileData, err := io.ReadAll(file); err == nil {
			newContent = string(fileData)
		}
		file.Close()
	}

	fileDiff := GitFileDiff{
		Path:       filePath,
		OldContent: oldContent,
		NewContent: newContent,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fileDiff)
}
