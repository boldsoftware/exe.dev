package claudetool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOutputIframeRun(t *testing.T) {
	tests := []struct {
		name      string
		input     map[string]any
		wantErr   bool
		wantTitle string
	}{
		{
			name: "basic html",
			input: map[string]any{
				"html": "<h1>Hello</h1>",
			},
			wantErr:   false,
			wantTitle: "",
		},
		{
			name: "html with title",
			input: map[string]any{
				"html":  "<div>Chart</div>",
				"title": "My Chart",
			},
			wantErr:   false,
			wantTitle: "My Chart",
		},
		{
			name: "empty html",
			input: map[string]any{
				"html": "",
			},
			wantErr: true,
		},
		{
			name:    "missing html",
			input:   map[string]any{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("failed to marshal input: %v", err)
			}

			result := outputIframeRun(context.Background(), inputJSON)

			if tt.wantErr {
				if result.Error == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if result.Error != nil {
				t.Errorf("unexpected error: %v", result.Error)
				return
			}

			if len(result.LLMContent) != 1 || result.LLMContent[0].Text != "displayed" {
				t.Errorf("expected LLMContent [displayed], got %v", result.LLMContent)
			}

			display, ok := result.Display.(OutputIframeDisplay)
			if !ok {
				t.Errorf("expected Display to be OutputIframeDisplay, got %T", result.Display)
				return
			}

			if display.Type != "output_iframe" {
				t.Errorf("expected Type 'output_iframe', got %q", display.Type)
			}

			if display.Title != tt.wantTitle {
				t.Errorf("expected Title %q, got %q", tt.wantTitle, display.Title)
			}

			if html, _ := tt.input["html"].(string); display.HTML != html {
				t.Errorf("expected HTML %q, got %q", html, display.HTML)
			}
		})
	}
}

func TestOutputIframeToolSchema(t *testing.T) {
	// Verify the tool is properly configured
	if OutputIframeTool.Name != "output_iframe" {
		t.Errorf("expected name 'output_iframe', got %q", OutputIframeTool.Name)
	}

	if OutputIframeTool.Run == nil {
		t.Error("expected Run function to be set")
	}

	if len(OutputIframeTool.InputSchema) == 0 {
		t.Error("expected InputSchema to be set")
	}
}
