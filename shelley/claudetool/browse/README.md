# Browser Tools for Claude

This package provides a set of tools that allow Claude to control a headless
Chrome browser from Go. The tools are built using the
[chromedp](https://github.com/chromedp/chromedp) library.

## Available Tools

1. `browser_navigate` - Navigate to a URL and wait for the page to load
2. `browser_eval` - Evaluate JavaScript in the browser context
3. `browser_screenshot` - Take a screenshot of the page or a specific element

## Usage

```go
// Create a context
ctx := context.Background()

// Register browser tools and get a cleanup function
tools, cleanup := browse.RegisterBrowserTools(ctx)
defer cleanup() // Important: always call cleanup to release browser resources

// Add tools to your agent
for _, tool := range tools {
    agent.AddTool(tool)
}
```

## Requirements

- Chrome or Chromium must be installed on the system
- In Docker environments, the multi-stage build automatically provides headless-shell from chromedp/headless-shell
- For local development, install Chrome/Chromium manually
- The `chromedp` package handles launching and controlling the browser

## Tool Input/Output

All tools follow a standard JSON input/output format. For example:

**Navigate Tool Input:**
```json
{
  "url": "https://example.com"
}
```

**Navigate Tool Output (success):**
```json
{
  "status": "success"
}
```

**Tool Output (error):**
```json
{
  "status": "error",
  "error": "Error message"
}
```

## Example Tool Usage

```go
// Example of using the navigate tool directly
navTool := tools[0] // Get browser_navigate tool
input := map[string]string{"url": "https://example.com"}
inputJSON, _ := json.Marshal(input)

// Call the tool
result, err := navTool.Run(ctx, json.RawMessage(inputJSON))
if err != nil {
    log.Fatalf("Error: %v", err)
}
fmt.Println(result)
```

## Screenshot Storage

The browser screenshot tool has been modified to save screenshots to a temporary directory and identify them by ID, rather than returning base64-encoded data directly. This improves efficiency by:

1. Reducing token usage in LLM responses
2. Avoiding encoding/decoding overhead
3. Allowing for larger screenshots without message size limitations

### How It Works

1. When a screenshot is taken, it's saved to `/tmp/shelley-screenshots/` with a unique UUID filename
2. The tool returns the screenshot ID in its response
3. The web UI can fetch the screenshot using the `/api/read?path=...` endpoint (with path set to the screenshot file)

### Example Usage

Agent calls the screenshot tool:
```json
{
  "id": "tool_call_123",
  "name": "browser_screenshot",
  "params": {}
}
```

Tool response:
```json
{
  "id": "tool_call_123",
  "result": {
    "id": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

The screenshot is then accessible at: `/api/read?path=/tmp/shelley-screenshots/550e8400-e29b-41d4-a716-446655440000.png`
