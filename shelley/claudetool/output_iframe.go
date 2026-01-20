package claudetool

import (
	"context"
	"encoding/json"

	"shelley.exe.dev/llm"
)

// OutputIframeTool displays sandboxed HTML content to the user.
var OutputIframeTool = &llm.Tool{
	Name:        outputIframeName,
	Description: outputIframeDescription,
	InputSchema: llm.MustSchema(outputIframeInputSchema),
	Run:         outputIframeRun,
}

const (
	outputIframeName        = "output_iframe"
	outputIframeDescription = `Display HTML content to the user in a sandboxed iframe.

Use this tool for visualizations like charts, graphs, and HTML demos that the user should see.
The HTML will be rendered in a secure sandbox with scripts enabled but isolated from the parent page.

Do NOT use this tool for:
- Regular text responses (use normal messages instead)
- File operations (use patch or bash)
- Simple data display (just describe it in text)

Good uses:
- Vega-Lite or other chart library visualizations  
- HTML/CSS demonstrations
- Interactive widgets or mini-apps
- SVG graphics

The HTML should be self-contained. You can include inline <script> and <style> tags.
External resources can be loaded via CDN (e.g., https://cdn.jsdelivr.net/).`

	outputIframeInputSchema = `
{
  "type": "object",
  "required": ["html"],
  "properties": {
    "html": {
      "type": "string",
      "description": "The HTML content to display. Should be a complete HTML document or fragment."
    },
    "title": {
      "type": "string", 
      "description": "Optional title describing the visualization"
    }
  }
}
`
)

// OutputIframeDisplay is the data passed to the UI for rendering.
type OutputIframeDisplay struct {
	Type  string `json:"type"`
	HTML  string `json:"html"`
	Title string `json:"title,omitempty"`
}

func outputIframeRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input struct {
		HTML  string `json:"html"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorToolOut(err)
	}

	if input.HTML == "" {
		return llm.ErrorfToolOut("html content is required")
	}

	display := OutputIframeDisplay{
		Type:  "output_iframe",
		HTML:  input.HTML,
		Title: input.Title,
	}

	return llm.ToolOut{
		LLMContent: llm.TextContent("displayed"),
		Display:    display,
	}
}
