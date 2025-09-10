package browse

import (
	"context"

	"shelley.exe.dev/llm"
)

// RegisterBrowserTools returns all browser tools ready to be added to an agent.
// It also returns a cleanup function that should be called when done to properly close the browser.
// The browser will be initialized lazily when a browser tool is first used.
func RegisterBrowserTools(ctx context.Context, supportsScreenshots bool) ([]*llm.Tool, func()) {
	browserTools := NewBrowseTools(ctx)

	return browserTools.GetTools(supportsScreenshots), func() {
		browserTools.Close()
	}
}

// Tool is an alias for llm.Tool to make the documentation clearer
type Tool = llm.Tool
