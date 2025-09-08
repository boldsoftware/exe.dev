package onstart

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestAnalyzeCodebase(t *testing.T) {
	t.Run("Basic Analysis", func(t *testing.T) {
		// Test basic functionality with regular ASCII filenames
		codebase, err := AnalyzeCodebase(context.Background(), ".")
		if err != nil {
			t.Fatalf("AnalyzeCodebase failed: %v", err)
		}

		if codebase == nil {
			t.Fatal("Expected non-nil codebase")
		}

		if codebase.TotalFiles == 0 {
			t.Error("Expected some files to be analyzed")
		}

		if len(codebase.ExtensionCounts) == 0 {
			t.Error("Expected extension counts to be populated")
		}
	})

	t.Run("Non-ASCII Filenames", func(t *testing.T) {
		// Create a temporary directory with unicode filenames for testing
		tempDir := t.TempDir()

		// Initialize git repository
		cmd := exec.Command("git", "init")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to init git repo: %v", err)
		}

		cmd = exec.Command("git", "config", "user.name", "Test User")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to set git user.name: %v", err)
		}

		cmd = exec.Command("git", "config", "user.email", "test@example.com")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to set git user.email: %v", err)
		}

		// Configure git to handle unicode filenames properly
		cmd = exec.Command("git", "config", "core.quotepath", "false")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to set git core.quotepath: %v", err)
		}

		cmd = exec.Command("git", "config", "core.precomposeunicode", "true")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to set git core.precomposeunicode: %v", err)
		}

		// Create test files with unicode characters dynamically
		testFiles := map[string]string{
			"测试文件.go":           "// Package test with Chinese characters in filename\npackage test\n\nfunc TestFunction() {\n\t// This is a test file\n}",
			"café.js":           "// JavaScript file with French characters\nconsole.log('Hello from café!');",
			"русский.py":        "# Python file with Russian characters\nprint('Привет мир!')",
			"🚀rocket.md":        "# README with Emoji\n\nThis file has an emoji in the filename.",
			"readme-español.md": "# Spanish README\n\nEste es un archivo de documentación.",
			"Übung.html":        "<!DOCTYPE html>\n<html><head><title>German Exercise</title></head><body><h1>Übung</h1></body></html>",
			"Makefile-日本語":      "# Japanese Makefile\nall:\n\techo 'Japanese makefile'",
		}

		// Create subdirectory
		subdir := filepath.Join(tempDir, "subdir")
		err := os.MkdirAll(subdir, 0o755)
		if err != nil {
			t.Fatalf("Failed to create subdir: %v", err)
		}

		// Add file in subdirectory
		testFiles["subdir/claude.한국어.md"] = "# Korean Claude file\n\nThis is a guidance file with Korean characters."

		// Write all test files
		for filename, content := range testFiles {
			fullPath := filepath.Join(tempDir, filename)
			dir := filepath.Dir(fullPath)
			if dir != tempDir {
				err := os.MkdirAll(dir, 0o755)
				if err != nil {
					t.Fatalf("Failed to create directory %s: %v", dir, err)
				}
			}
			err := os.WriteFile(fullPath, []byte(content), 0o644)
			if err != nil {
				t.Fatalf("Failed to write file %s: %v", filename, err)
			}
		}

		// Add all files to git at once
		cmd = exec.Command("git", "add", ".")
		cmd.Dir = tempDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to add files to git: %v", err)
		}

		// Test with non-ASCII characters in filenames
		codebase, err := AnalyzeCodebase(context.Background(), tempDir)
		if err != nil {
			t.Fatalf("AnalyzeCodebase failed with non-ASCII filenames: %v", err)
		}

		if codebase == nil {
			t.Fatal("Expected non-nil codebase")
		}

		// We expect 8 files in our temp directory
		expectedFiles := 8
		if codebase.TotalFiles != expectedFiles {
			t.Errorf("Expected %d files, got %d", expectedFiles, codebase.TotalFiles)
		}

		// Verify extension counts include our non-ASCII files
		expectedExtensions := map[string]int{
			".go":            1, // 测试文件.go
			".js":            1, // café.js
			".py":            1, // русский.py
			".md":            3, // 🚀rocket.md, readme-español.md, claude.한국어.md
			".html":          1, // Übung.html
			"<no-extension>": 1, // Makefile-日本語
		}

		for ext, expectedCount := range expectedExtensions {
			actualCount, exists := codebase.ExtensionCounts[ext]
			if !exists {
				t.Errorf("Expected extension %s to be found", ext)
				continue
			}
			if actualCount != expectedCount {
				t.Errorf("Expected %d files with extension %s, got %d", expectedCount, ext, actualCount)
			}
		}

		// Verify file categorization works with non-ASCII filenames
		// Check build files
		if !slices.Contains(codebase.BuildFiles, "Makefile-日本語") {
			t.Error("Expected Makefile-日本語 to be categorized as a build file")
		}

		// Check documentation files
		if !slices.Contains(codebase.DocumentationFiles, "readme-español.md") {
			t.Error("Expected readme-español.md to be categorized as a documentation file")
		}

		// Check guidance files
		if !slices.Contains(codebase.GuidanceFiles, "subdir/claude.한국어.md") {
			t.Error("Expected subdir/claude.한국어.md to be categorized as a guidance file")
		}
	})
}

func TestCategorizeFile(t *testing.T) {
	t.Run("Non-ASCII Filenames", func(t *testing.T) {
		tests := []struct {
			name     string
			path     string
			expected string
		}{
			{"Chinese Go file", "测试文件.go", ""},
			{"French JS file", "café.js", ""},
			{"Russian Python file", "русский.py", ""},
			{"Emoji markdown file", "🚀rocket.md", ""},
			{"German HTML file", "Übung.html", ""},
			{"Japanese Makefile", "Makefile-日本語", "build"},
			{"Spanish README", "readme-español.md", "documentation"},
			{"Korean Claude file", "subdir/claude.한국어.md", "guidance"},
			// Test edge cases with Unicode normalization and combining characters
			{"Mixed Unicode file", "test中文🚀.txt", ""},
			{"Combining characters", "filé̂.go", ""}, // file with combining acute and circumflex accents
			{"Right-to-left script", "مرحبا.py", ""},  // Arabic "hello"
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := categorizeFile(tt.path)
				if result != tt.expected {
					t.Errorf("categorizeFile(%q) = %q, want %q", tt.path, result, tt.expected)
				}
			})
		}
	})
}

func TestTopExtensions(t *testing.T) {
	t.Run("With Non-ASCII Files", func(t *testing.T) {
		// Create a test codebase with known extension counts
		codebase := &Codebase{
			ExtensionCounts: map[string]int{
				".md":   5, // Most common
				".go":   3,
				".js":   2,
				".py":   1,
				".html": 1, // Least common
			},
			TotalFiles: 12,
		}

		topExt := codebase.TopExtensions()
		if len(topExt) != 5 {
			t.Errorf("Expected 5 top extensions, got %d", len(topExt))
		}

		// Check that extensions are sorted by count (descending)
		expected := []string{
			".md: 5 (42%)",
			".go: 3 (25%)",
			".js: 2 (17%)",
			".html: 1 (8%)",
			".py: 1 (8%)",
		}

		for i, expectedExt := range expected {
			if i >= len(topExt) {
				t.Errorf("Missing expected extension at index %d: %s", i, expectedExt)
				continue
			}
			if topExt[i] != expectedExt {
				t.Errorf("Expected extension %q at index %d, got %q", expectedExt, i, topExt[i])
			}
		}
	})
}
