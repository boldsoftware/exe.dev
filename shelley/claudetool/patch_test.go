package claudetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestPatchTool_BasicOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test overwrite operation (creates new file)
	testFile := filepath.Join(tempDir, "test.txt")
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "Hello World\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("overwrite failed: %v", result.Error)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "Hello World\n" {
		t.Errorf("expected 'Hello World\\n', got %q", string(content))
	}

	// Test replace operation
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "World",
		NewText:   "Patch",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("replace failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	if string(content) != "Hello Patch\n" {
		t.Errorf("expected 'Hello Patch\\n', got %q", string(content))
	}

	// Test append_eof operation
	input.Patches = []PatchRequest{{
		Operation: "append_eof",
		NewText:   "Appended line\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("append_eof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected := "Hello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}

	// Test prepend_bof operation
	input.Patches = []PatchRequest{{
		Operation: "prepend_bof",
		NewText:   "Prepended line\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("prepend_bof failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	expected = "Prepended line\nHello Patch\nAppended line\n"
	if string(content) != expected {
		t.Errorf("expected %q, got %q", expected, string(content))
	}
}

func TestPatchTool_ClipboardOperations(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "clipboard.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "function original() {\n    return 'original';\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial overwrite failed: %v", result.Error)
	}

	// Test toClipboard operation
	input.Patches = []PatchRequest{{
		Operation:   "replace",
		OldText:     "function original() {\n    return 'original';\n}",
		NewText:     "function renamed() {\n    return 'renamed';\n}",
		ToClipboard: "saved_func",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("toClipboard failed: %v", result.Error)
	}

	// Test fromClipboard operation
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "saved_func",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("fromClipboard failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "function original()") {
		t.Error("clipboard content not restored properly")
	}
}

func TestPatchTool_IndentationAdjustment(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "indent.go")

	// Create file with tab indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc main() {\n\tif true {\n\t\t// placeholder\n\t}\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test indentation adjustment: convert spaces to tabs
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "// placeholder",
		NewText:   "    fmt.Println(\"hello\")\n    fmt.Println(\"world\")",
		Reindent: &Reindent{
			Strip: "    ",
			Add:   "\t\t",
		},
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("indentation adjustment failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	expected := "\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")"
	if !strings.Contains(string(content), expected) {
		t.Errorf("indentation not adjusted correctly, got:\n%s", string(content))
	}
}

func TestPatchTool_FuzzyMatching(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "fuzzy.go")

	// Create Go file with specific indentation
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc test() {\n\tif condition {\n\t\tfmt.Println(\"hello\")\n\t\tfmt.Println(\"world\")\n\t}\n}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("initial setup failed: %v", result.Error)
	}

	// Test fuzzy matching with different whitespace
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "if condition {\n        fmt.Println(\"hello\")\n        fmt.Println(\"world\")\n    }", // spaces instead of tabs
		NewText:   "if condition {\n\t\tfmt.Println(\"modified\")\n\t}",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("fuzzy matching failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "modified") {
		t.Error("fuzzy matching did not work")
	}
}

func TestPatchTool_ErrorCases(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "error.txt")

	// Test replace operation on non-existent file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "replace",
			OldText:   "something",
			NewText:   "else",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error == nil {
		t.Error("expected error for replace on non-existent file")
	}

	// Create file with duplicate text
	input.Patches = []PatchRequest{{
		Operation: "overwrite",
		NewText:   "duplicate\nduplicate\n",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create test file: %v", result.Error)
	}

	// Test non-unique text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "duplicate",
		NewText:   "unique",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not unique") {
		t.Error("expected non-unique error")
	}

	// Test missing text
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "nonexistent",
		NewText:   "something",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "not found") {
		t.Error("expected not found error")
	}

	// Test invalid clipboard reference
	input.Patches = []PatchRequest{{
		Operation:     "append_eof",
		FromClipboard: "nonexistent",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "clipboard") {
		t.Error("expected clipboard error")
	}
}

func TestPatchTool_FlexibleInputParsing(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "flexible.txt")

	// Test single patch format (PatchInputOne)
	inputOne := PatchInputOne{
		Path: testFile,
		Patches: &PatchRequest{
			Operation: "overwrite",
			NewText:   "Single patch format\n",
		},
	}

	msg, _ := json.Marshal(inputOne)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("single patch format failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "Single patch format\n" {
		t.Error("single patch format did not work")
	}

	// Test string patch format (PatchInputOneString)
	patchStr := `{"operation": "replace", "oldText": "Single", "newText": "Modified"}`
	inputStr := PatchInputOneString{
		Path:    testFile,
		Patches: patchStr,
	}

	msg, _ = json.Marshal(inputStr)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("string patch format failed: %v", result.Error)
	}

	content, _ = os.ReadFile(testFile)
	if !strings.Contains(string(content), "Modified") {
		t.Error("string patch format did not work")
	}
}

func TestPatchTool_AutogeneratedDetection(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "generated.go")

	// Create autogenerated file
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "// Code generated by tool. DO NOT EDIT.\npackage main\n\nfunc generated() {}\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create generated file: %v", result.Error)
	}

	// Test patching autogenerated file (should warn but work)
	input.Patches = []PatchRequest{{
		Operation: "replace",
		OldText:   "func generated() {}",
		NewText:   "func modified() {}",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("patching generated file failed: %v", result.Error)
	}

	if len(result.LLMContent) == 0 || !strings.Contains(result.LLMContent[0].Text, "autogenerated") {
		t.Error("expected autogenerated warning")
	}
}

func TestPatchTool_MultiplePatches(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "multi.go")
	var msg []byte
	var result llm.ToolOut

	// Apply multiple patches - first create file, then modify
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "package main\n\nfunc first() {\n\tprintln(\"first\")\n}\n\nfunc second() {\n\tprintln(\"second\")\n}\n",
		}},
	}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create initial file: %v", result.Error)
	}

	// Now apply multiple patches in one call
	input.Patches = []PatchRequest{
		{
			Operation: "replace",
			OldText:   "println(\"first\")",
			NewText:   "println(\"ONE\")",
		},
		{
			Operation: "replace",
			OldText:   "println(\"second\")",
			NewText:   "println(\"TWO\")",
		},
		{
			Operation: "append_eof",
			NewText:   "\n// Multiple patches applied\n",
		},
	}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("multiple patches failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	contentStr := string(content)
	if !strings.Contains(contentStr, "ONE") || !strings.Contains(contentStr, "TWO") {
		t.Error("multiple patches not applied correctly")
	}
	if !strings.Contains(contentStr, "Multiple patches applied") {
		t.Error("append_eof in multiple patches not applied")
	}
}

func TestPatchTool_CopyRecipe(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "copy.txt")

	// Create initial content
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "original text",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("failed to create file: %v", result.Error)
	}

	// Test copy recipe (toClipboard + fromClipboard with same name)
	input.Patches = []PatchRequest{{
		Operation:     "replace",
		OldText:       "original text",
		NewText:       "replaced text",
		ToClipboard:   "copy_test",
		FromClipboard: "copy_test",
	}}

	msg, _ = json.Marshal(input)
	result = patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("copy recipe failed: %v", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	// The copy recipe should preserve the original text
	if string(content) != "original text" {
		t.Errorf("copy recipe failed, expected 'original text', got %q", string(content))
	}
}

func TestPatchTool_RelativePaths(t *testing.T) {
	tempDir := t.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	// Test relative path resolution
	input := PatchInput{
		Path: "relative.txt", // relative path
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "relative path test\n",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)
	if result.Error != nil {
		t.Fatalf("relative path failed: %v", result.Error)
	}

	// Check file was created in correct location
	expectedPath := filepath.Join(tempDir, "relative.txt")
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not created at expected path: %v", err)
	}
	if string(content) != "relative path test\n" {
		t.Error("relative path file content incorrect")
	}
}

// Benchmark basic patch operations
func BenchmarkPatchTool_BasicOperations(b *testing.B) {
	tempDir := b.TempDir()
	patch := &PatchTool{WorkingDir: NewMutableWorkingDir(tempDir)}
	ctx := context.Background()

	testFile := filepath.Join(tempDir, "bench.go")
	initialContent := "package main\n\nfunc test() {\n\tfor i := 0; i < 100; i++ {\n\t\tfmt.Println(i)\n\t}\n}\n"

	// Setup
	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   initialContent,
		}},
	}
	msg, _ := json.Marshal(input)
	patch.Run(ctx, msg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Benchmark replace operation
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Println(i)",
			NewText:   "fmt.Printf(\"%d\\n\", i)",
		}}

		msg, _ := json.Marshal(input)
		result := patch.Run(ctx, msg)
		if result.Error != nil {
			b.Fatalf("benchmark failed: %v", result.Error)
		}

		// Reset for next iteration
		input.Patches = []PatchRequest{{
			Operation: "replace",
			OldText:   "fmt.Printf(\"%d\\n\", i)",
			NewText:   "fmt.Println(i)",
		}}
		msg, _ = json.Marshal(input)
		patch.Run(ctx, msg)
	}
}

func TestPatchTool_CallbackFunction(t *testing.T) {
	tempDir := t.TempDir()
	callbackCalled := false
	var capturedInput PatchInput
	var capturedOutput llm.ToolOut

	patch := &PatchTool{
		WorkingDir: NewMutableWorkingDir(tempDir),
		Callback: func(input PatchInput, output llm.ToolOut) llm.ToolOut {
			callbackCalled = true
			capturedInput = input
			capturedOutput = output
			// Modify the output
			output.LLMContent = llm.TextContent("Modified by callback")
			return output
		},
	}

	ctx := context.Background()
	testFile := filepath.Join(tempDir, "callback.txt")

	input := PatchInput{
		Path: testFile,
		Patches: []PatchRequest{{
			Operation: "overwrite",
			NewText:   "callback test",
		}},
	}

	msg, _ := json.Marshal(input)
	result := patch.Run(ctx, msg)

	if !callbackCalled {
		t.Error("callback was not called")
	}

	if capturedInput.Path != testFile {
		t.Error("callback did not receive correct input")
	}

	if len(result.LLMContent) == 0 || result.LLMContent[0].Text != "Modified by callback" {
		t.Error("callback did not modify output correctly")
	}

	if capturedOutput.Error != nil {
		t.Errorf("callback received error: %v", capturedOutput.Error)
	}
}
