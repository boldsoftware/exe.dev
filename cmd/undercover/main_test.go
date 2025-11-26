package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUndercover(t *testing.T) {
	tmpDir := t.TempDir()

	sourceFile := filepath.Join(tmpDir, "example.go")
	err := os.WriteFile(sourceFile, []byte(`package example

func Add(a, b int) int {
	return a + b
}

func Sub(a, b int) int {
	return a - b
}

func Mul(a, b int) int {
	return a * b
}
`), 0o644)
	require.NoError(t, err)

	cov1 := filepath.Join(tmpDir, "test1.cov")
	err = os.WriteFile(cov1, []byte(`mode: set
`+sourceFile+`:3.25,5.2 1 1
`+sourceFile+`:7.25,9.2 1 0
`+sourceFile+`:11.25,13.2 1 0
`), 0o644)
	require.NoError(t, err)

	cov2 := filepath.Join(tmpDir, "test2.cov")
	err = os.WriteFile(cov2, []byte(`mode: set
`+sourceFile+`:3.25,5.2 1 1
`+sourceFile+`:7.25,9.2 1 1
`+sourceFile+`:11.25,13.2 1 0
`), 0o644)
	require.NoError(t, err)

	cov3 := filepath.Join(tmpDir, "test3.cov")
	err = os.WriteFile(cov3, []byte(`mode: set
`+sourceFile+`:3.25,5.2 1 1
`+sourceFile+`:7.25,9.2 1 1
`+sourceFile+`:11.25,13.2 1 1
`), 0o644)
	require.NoError(t, err)

	outputHTML := filepath.Join(tmpDir, "coverage.html")

	cmd := exec.Command("go", "run", ".", "-o", outputHTML, cov1, cov2, cov3)
	cmd.Dir = filepath.Join(".", "")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command output: %s", output)
	}
	require.NoError(t, err)

	htmlBytes, err := os.ReadFile(outputHTML)
	require.NoError(t, err)
	html := string(htmlBytes)

	require.Contains(t, html, "Go Coverage Report")
	require.Contains(t, html, sourceFile)
	require.Contains(t, html, cov1)
	require.Contains(t, html, cov2)
	require.Contains(t, html, cov3)

	require.Contains(t, html, "func Add(a, b int) int")
	require.Contains(t, html, "func Sub(a, b int) int")
	require.Contains(t, html, "func Mul(a, b int) int")

	require.Contains(t, html, "Filter by input")
	require.Contains(t, html, "Covered by:")

	lines := strings.Split(html, "\n")
	var inputOrder []string
	for _, line := range lines {
		if strings.Contains(line, "<option value=") && strings.Contains(line, ".cov") {
			if strings.Contains(line, cov1) {
				inputOrder = append(inputOrder, "cov1")
			} else if strings.Contains(line, cov2) {
				inputOrder = append(inputOrder, "cov2")
			} else if strings.Contains(line, cov3) {
				inputOrder = append(inputOrder, "cov3")
			}
		}
	}

	require.Len(t, inputOrder, 3)
	require.Equal(t, "cov1", inputOrder[0], "test1.cov should be first (least coverage)")
	require.Equal(t, "cov2", inputOrder[1], "test2.cov should be second")
	require.Equal(t, "cov3", inputOrder[2], "test3.cov should be last (most coverage)")
}

func TestParseCoverageFiles(t *testing.T) {
	tmpDir := t.TempDir()

	sourceFile := filepath.Join(tmpDir, "test.go")
	err := os.WriteFile(sourceFile, []byte(`package test

func Foo() {
	println("foo")
}
`), 0o644)
	require.NoError(t, err)

	cov1 := filepath.Join(tmpDir, "input1.cov")
	err = os.WriteFile(cov1, []byte(`mode: set
`+sourceFile+`:3.12,5.2 1 1
`), 0o644)
	require.NoError(t, err)

	inputs, err := parseInputs([]string{cov1})
	require.NoError(t, err)
	require.Len(t, inputs, 1)

	input := inputs[0]
	require.Equal(t, cov1, input.Name)
	require.Contains(t, input.Profiles, sourceFile)

	profile := input.Profiles[sourceFile]
	require.Equal(t, sourceFile, profile.FileName)
	require.Equal(t, "set", profile.Mode)
	require.Len(t, profile.Blocks, 1)

	block := profile.Blocks[0]
	require.Equal(t, 3, block.StartLine)
	require.Equal(t, 12, block.StartCol)
	require.Equal(t, 5, block.EndLine)
	require.Equal(t, 2, block.EndCol)
	require.Equal(t, 1, block.NumStmt)
	require.Equal(t, 1, block.Count)
}

func TestSortInputsByCoverage(t *testing.T) {
	inputs := []*InputCoverage{
		{Name: "high", TotalLines: 100, CoveredLines: 90},
		{Name: "low", TotalLines: 100, CoveredLines: 30},
		{Name: "medium", TotalLines: 100, CoveredLines: 60},
	}

	sortInputs(inputs)

	require.Equal(t, "low", inputs[0].Name)
	require.Equal(t, "medium", inputs[1].Name)
	require.Equal(t, "high", inputs[2].Name)
}

func TestAggregateCoverage(t *testing.T) {
	tmpDir := t.TempDir()

	sourceFile := filepath.Join(tmpDir, "code.go")
	err := os.WriteFile(sourceFile, []byte(`package code

func A() {}
func B() {}
`), 0o644)
	require.NoError(t, err)

	inputs := []*InputCoverage{
		{
			Name: "input1",
			Profiles: map[string]*Profile{
				sourceFile: {
					FileName: sourceFile,
					Blocks: []ProfileBlock{
						{StartLine: 3, EndLine: 3, Count: 1},
					},
				},
			},
		},
		{
			Name: "input2",
			Profiles: map[string]*Profile{
				sourceFile: {
					FileName: sourceFile,
					Blocks: []ProfileBlock{
						{StartLine: 3, EndLine: 3, Count: 1},
						{StartLine: 4, EndLine: 4, Count: 1},
					},
				},
			},
		},
	}

	files, err := aggregateFileCoverage(inputs)
	require.NoError(t, err)
	require.Contains(t, files, sourceFile)

	fc := files[sourceFile]
	require.Equal(t, sourceFile, fc.FileName)
	require.Len(t, fc.Inputs, 2)

	require.True(t, fc.LineInfo[3].Covered)
	require.True(t, fc.LineInfo[3].CoveredBy["input1"])
	require.True(t, fc.LineInfo[3].CoveredBy["input2"])

	require.True(t, fc.LineInfo[4].Covered)
	require.False(t, fc.LineInfo[4].CoveredBy["input1"])
	require.True(t, fc.LineInfo[4].CoveredBy["input2"])
}

func TestCLIArguments(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		shouldErr bool
	}{
		{
			name:      "no arguments",
			args:      []string{},
			shouldErr: true,
		},
		{
			name:      "missing output",
			args:      []string{"input.cov"},
			shouldErr: true,
		},
		{
			name:      "missing input",
			args:      []string{"-o", "out.html"},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("go", append([]string{"run", "."}, tt.args...)...)
			cmd.Dir = filepath.Join(".", "")
			output, err := cmd.CombinedOutput()

			if tt.shouldErr {
				require.Error(t, err, "output: %s", output)
			} else {
				require.NoError(t, err, "output: %s", output)
			}
		})
	}
}

func TestMultipleFilesAndInputs(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.go")
	err := os.WriteFile(file1, []byte(`package test

func File1Func() {
	println("file1")
}
`), 0o644)
	require.NoError(t, err)

	file2 := filepath.Join(tmpDir, "file2.go")
	err = os.WriteFile(file2, []byte(`package test

func File2Func() {
	println("file2")
}
`), 0o644)
	require.NoError(t, err)

	cov1 := filepath.Join(tmpDir, "cov1.cov")
	err = os.WriteFile(cov1, []byte(`mode: set
`+file1+`:3.18,5.2 1 1
`), 0o644)
	require.NoError(t, err)

	cov2 := filepath.Join(tmpDir, "cov2.cov")
	err = os.WriteFile(cov2, []byte(`mode: set
`+file2+`:3.18,5.2 1 1
`), 0o644)
	require.NoError(t, err)

	outputHTML := filepath.Join(tmpDir, "multi.html")

	cmd := exec.Command("go", "run", ".", "-o", outputHTML, cov1, cov2)
	cmd.Dir = filepath.Join(".", "")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Command output: %s", output)
	}
	require.NoError(t, err)

	htmlBytes, err := os.ReadFile(outputHTML)
	require.NoError(t, err)
	html := string(htmlBytes)

	require.Contains(t, html, file1)
	require.Contains(t, html, file2)
	require.Contains(t, html, "File1Func")
	require.Contains(t, html, "File2Func")
}
