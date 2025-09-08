// Package browse provides browser automation tools for the agent
package browse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"sketch.dev/llm"
)

// ScreenshotDir is the directory where screenshots are stored
const ScreenshotDir = "/tmp/sketch-screenshots"

// BrowseTools contains all browser tools and manages a shared browser instance
type BrowseTools struct {
	ctx              context.Context
	cancel           context.CancelFunc
	browserCtx       context.Context
	browserCtxCancel context.CancelFunc
	mux              sync.Mutex
	initOnce         sync.Once
	initialized      bool
	initErr          error
	// Map to track screenshots by ID and their creation time
	screenshots      map[string]time.Time
	screenshotsMutex sync.Mutex
	// Console logs storage
	consoleLogs      []*runtime.EventConsoleAPICalled
	consoleLogsMutex sync.Mutex
	maxConsoleLogs   int
}

// NewBrowseTools creates a new set of browser automation tools
func NewBrowseTools(ctx context.Context) *BrowseTools {
	ctx, cancel := context.WithCancel(ctx)

	// Ensure the screenshot directory exists
	if err := os.MkdirAll(ScreenshotDir, 0o755); err != nil {
		log.Printf("Failed to create screenshot directory: %v", err)
	}

	b := &BrowseTools{
		ctx:            ctx,
		cancel:         cancel,
		screenshots:    make(map[string]time.Time),
		consoleLogs:    make([]*runtime.EventConsoleAPICalled, 0),
		maxConsoleLogs: 100,
	}

	return b
}

// Initialize starts the browser if it's not already running
func (b *BrowseTools) Initialize() error {
	b.mux.Lock()
	defer b.mux.Unlock()

	b.initOnce.Do(func() {
		// ChromeDP.ExecPath has a list of common places to find Chrome...
		opts := chromedp.DefaultExecAllocatorOptions[:]
		// This is the default when running as root, but we generally need it
		// when running in a container, even when we aren't root (which is largely
		// the case for tests).
		opts = append(opts, chromedp.NoSandbox)
		// Setting 'DBUS_SESSION_BUS_ADDRESS=""' or this flag allows tests to pass
		// in GitHub runner contexts. It's a mystery why the failure isn't clear when this fails.
		opts = append(opts, chromedp.Flag("--disable-dbus", true))
		// This can be pretty slow in tests
		opts = append(opts, chromedp.WSURLReadTimeout(60*time.Second))
		// Add environment variable to mark this as a sketch internal process
		opts = append(opts, chromedp.Env("SKETCH_IGNORE_PORTS=1"))
		allocCtx, _ := chromedp.NewExecAllocator(b.ctx, opts...)
		browserCtx, browserCancel := chromedp.NewContext(
			allocCtx,
			chromedp.WithLogf(log.Printf), chromedp.WithErrorf(log.Printf), chromedp.WithBrowserOption(chromedp.WithDialTimeout(60*time.Second)),
		)

		b.browserCtx = browserCtx
		b.browserCtxCancel = browserCancel

		// Set up console log listener
		chromedp.ListenTarget(browserCtx, func(ev any) {
			switch e := ev.(type) {
			case *runtime.EventConsoleAPICalled:
				b.captureConsoleLog(e)
			}
		})

		// Ensure the browser starts
		if err := chromedp.Run(browserCtx); err != nil {
			b.initErr = fmt.Errorf("failed to start browser (please apt get chromium or equivalent): %w", err)
			return
		}

		// Set default viewport size to 1280x720 (16:9 widescreen)
		if err := chromedp.Run(browserCtx, chromedp.EmulateViewport(1280, 720)); err != nil {
			b.initErr = fmt.Errorf("failed to set default viewport: %w", err)
			return
		}

		b.initialized = true
	})

	return b.initErr
}

// Close shuts down the browser
func (b *BrowseTools) Close() {
	b.mux.Lock()
	defer b.mux.Unlock()

	if b.browserCtxCancel != nil {
		b.browserCtxCancel()
		b.browserCtxCancel = nil
	}

	if b.cancel != nil {
		b.cancel()
	}

	b.initialized = false
	log.Println("Browser closed")
}

// GetBrowserContext returns the context for browser operations
func (b *BrowseTools) GetBrowserContext() (context.Context, error) {
	if err := b.Initialize(); err != nil {
		return nil, err
	}
	return b.browserCtx, nil
}

// NavigateTool definition
type navigateInput struct {
	URL     string `json:"url"`
	Timeout string `json:"timeout,omitempty"`
}

// isPort80 reports whether urlStr definitely uses port 80.
func isPort80(urlStr string) bool {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	port := parsedURL.Port()
	return port == "80" || (port == "" && parsedURL.Scheme == "http")
}

// NewNavigateTool creates a tool for navigating to URLs
func (b *BrowseTools) NewNavigateTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_navigate",
		Description: "Navigate the browser to a specific URL and wait for page to load",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "The URL to navigate to"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			},
			"required": ["url"]
		}`),
		Run: b.navigateRun,
	}
}

func (b *BrowseTools) navigateRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input navigateInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	if isPort80(input.URL) {
		return llm.ErrorToolOut(fmt.Errorf("port 80 is not the port you're looking for--port 80 is the main sketch server"))
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(input.URL),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("done")}
}

// EvalTool definition
type evalInput struct {
	Expression string `json:"expression"`
	Timeout    string `json:"timeout,omitempty"`
	Await      *bool  `json:"await,omitempty"`
}

// NewEvalTool creates a tool for evaluating JavaScript
func (b *BrowseTools) NewEvalTool() *llm.Tool {
	return &llm.Tool{
		Name: "browser_eval",
		Description: `Evaluate JavaScript in the browser context.
Your go-to tool for interacting with content: clicking buttons, typing, getting content, scrolling, resizing, waiting for content/selector to be ready, etc.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "JavaScript expression to evaluate"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				},
				"await": {
					"type": "boolean",
					"description": "If true, wait for promises to resolve and return their resolved value (default: true)"
				}
			},
			"required": ["expression"]
		}`),
		Run: b.evalRun,
	}
}

func (b *BrowseTools) evalRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input evalInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	var result any
	var evalOps []chromedp.EvaluateOption

	await := true
	if input.Await != nil {
		await = *input.Await
	}
	if await {
		evalOps = append(evalOps, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		})
	}

	evalAction := chromedp.Evaluate(input.Expression, &result, evalOps...)

	err = chromedp.Run(timeoutCtx, evalAction)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Return the result as JSON
	response, err := json.Marshal(result)
	if err != nil {
		return llm.ErrorfToolOut("failed to marshal response: %w", err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("<javascript_result>" + string(response) + "</javascript_result>")}
}

// ScreenshotTool definition
type screenshotInput struct {
	Selector string `json:"selector,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

// NewScreenshotTool creates a tool for taking screenshots
func (b *BrowseTools) NewScreenshotTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_take_screenshot",
		Description: "Take a screenshot of the page or a specific element",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"selector": {
					"type": "string",
					"description": "CSS selector for the element to screenshot (optional)"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			}
		}`),
		Run: b.screenshotRun,
	}
}

func (b *BrowseTools) screenshotRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input screenshotInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Create a timeout context for this operation
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(input.Timeout))
	defer cancel()

	var buf []byte
	var actions []chromedp.Action

	if input.Selector != "" {
		// Take screenshot of specific element
		actions = append(actions,
			chromedp.WaitReady(input.Selector),
			chromedp.Screenshot(input.Selector, &buf, chromedp.NodeVisible),
		)
	} else {
		// Take full page screenshot
		actions = append(actions, chromedp.CaptureScreenshot(&buf))
	}

	err = chromedp.Run(timeoutCtx, actions...)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Save the screenshot and get its ID for potential future reference
	id := b.SaveScreenshot(buf)
	if id == "" {
		return llm.ErrorToolOut(fmt.Errorf("failed to save screenshot"))
	}

	// Get the full path to the screenshot
	screenshotPath := GetScreenshotPath(id)

	// Encode the image as base64
	base64Data := base64.StdEncoding.EncodeToString(buf)

	// Return the screenshot directly to the LLM
	return llm.ToolOut{LLMContent: []llm.Content{
		{
			Type: llm.ContentTypeText,
			Text: fmt.Sprintf("Screenshot taken (saved as %s)", screenshotPath),
		},
		{
			Type:      llm.ContentTypeText, // Will be mapped to image in content array
			MediaType: "image/png",
			Data:      base64Data,
		},
	}}
}

// GetTools returns browser tools, optionally filtering out screenshot-related tools
func (b *BrowseTools) GetTools(includeScreenshotTools bool) []*llm.Tool {
	tools := []*llm.Tool{
		b.NewNavigateTool(),
		b.NewEvalTool(),
		b.NewRecentConsoleLogsTool(),
		b.NewClearConsoleLogsTool(),
	}

	// Add screenshot-related tools if supported
	if includeScreenshotTools {
		tools = append(tools, b.NewScreenshotTool())
		tools = append(tools, b.NewReadImageTool())
	}

	return tools
}

// SaveScreenshot saves a screenshot to disk and returns its ID
func (b *BrowseTools) SaveScreenshot(data []byte) string {
	// Generate a unique ID
	id := uuid.New().String()

	// Save the file
	filePath := filepath.Join(ScreenshotDir, id+".png")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		log.Printf("Failed to save screenshot: %v", err)
		return ""
	}

	// Track this screenshot
	b.screenshotsMutex.Lock()
	b.screenshots[id] = time.Now()
	b.screenshotsMutex.Unlock()

	return id
}

// GetScreenshotPath returns the full path to a screenshot by ID
func GetScreenshotPath(id string) string {
	return filepath.Join(ScreenshotDir, id+".png")
}

// ReadImageTool definition
type readImageInput struct {
	Path    string `json:"path"`
	Timeout string `json:"timeout,omitempty"`
}

// NewReadImageTool creates a tool for reading images and returning them as base64 encoded data
func (b *BrowseTools) NewReadImageTool() *llm.Tool {
	return &llm.Tool{
		Name:        "read_image",
		Description: "Read an image file (such as a screenshot) and encode it for sending to the LLM",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "Path to the image file to read"
				},
				"timeout": {
					"type": "string",
					"description": "Timeout as a Go duration string (default: 15s)"
				}
			},
			"required": ["path"]
		}`),
		Run: b.readImageRun,
	}
}

func (b *BrowseTools) readImageRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input readImageInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Check if the path exists
	if _, err := os.Stat(input.Path); os.IsNotExist(err) {
		return llm.ErrorfToolOut("image file not found: %s", input.Path)
	}

	// Read the file
	imageData, err := os.ReadFile(input.Path)
	if err != nil {
		return llm.ErrorfToolOut("failed to read image file: %w", err)
	}

	// Detect the image type
	imageType := http.DetectContentType(imageData)
	if !strings.HasPrefix(imageType, "image/") {
		return llm.ErrorfToolOut("file is not an image: %s", imageType)
	}

	// Encode the image as base64
	base64Data := base64.StdEncoding.EncodeToString(imageData)

	// Create a Content object that includes both text and the image
	return llm.ToolOut{LLMContent: []llm.Content{
		{
			Type: llm.ContentTypeText,
			Text: fmt.Sprintf("Image from %s (type: %s)", input.Path, imageType),
		},
		{
			Type:      llm.ContentTypeText, // Will be mapped to image in content array
			MediaType: imageType,
			Data:      base64Data,
		},
	}}
}

// parseTimeout parses a timeout string and returns a time.Duration
// It returns a default of 5 seconds if the timeout is empty or invalid
func parseTimeout(timeout string) time.Duration {
	dur, err := time.ParseDuration(timeout)
	if err != nil {
		return 15 * time.Second
	}
	return dur
}

// captureConsoleLog captures a console log event and stores it
func (b *BrowseTools) captureConsoleLog(e *runtime.EventConsoleAPICalled) {
	// Add to logs with mutex protection
	b.consoleLogsMutex.Lock()
	defer b.consoleLogsMutex.Unlock()

	// Add the log and maintain max size
	b.consoleLogs = append(b.consoleLogs, e)
	if len(b.consoleLogs) > b.maxConsoleLogs {
		b.consoleLogs = b.consoleLogs[len(b.consoleLogs)-b.maxConsoleLogs:]
	}
}

// RecentConsoleLogsTool definition
type recentConsoleLogsInput struct {
	Limit int `json:"limit,omitempty"`
}

// NewRecentConsoleLogsTool creates a tool for retrieving recent console logs
func (b *BrowseTools) NewRecentConsoleLogsTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_recent_console_logs",
		Description: "Get recent browser console logs",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {
					"type": "integer",
					"description": "Maximum number of log entries to return (default: 100)"
				}
			}
		}`),
		Run: b.recentConsoleLogsRun,
	}
}

func (b *BrowseTools) recentConsoleLogsRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input recentConsoleLogsInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Apply limit (default to 100 if not specified)
	limit := 100
	if input.Limit > 0 {
		limit = input.Limit
	}

	// Get console logs with mutex protection
	b.consoleLogsMutex.Lock()
	logs := make([]*runtime.EventConsoleAPICalled, 0, len(b.consoleLogs))
	start := 0
	if len(b.consoleLogs) > limit {
		start = len(b.consoleLogs) - limit
	}
	logs = append(logs, b.consoleLogs[start:]...)
	b.consoleLogsMutex.Unlock()

	// Format the logs as JSON
	logData, err := json.MarshalIndent(logs, "", "  ")
	if err != nil {
		return llm.ErrorfToolOut("failed to serialize logs: %w", err)
	}

	// Format the logs
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Retrieved %d console log entries:\n\n", len(logs)))

	if len(logs) == 0 {
		sb.WriteString("No console logs captured.")
	} else {
		// Add the JSON data for full details
		sb.WriteString(string(logData))
	}

	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

// ClearConsoleLogsTool definition
type clearConsoleLogsInput struct{}

// NewClearConsoleLogsTool creates a tool for clearing console logs
func (b *BrowseTools) NewClearConsoleLogsTool() *llm.Tool {
	return &llm.Tool{
		Name:        "browser_clear_console_logs",
		Description: "Clear all captured browser console logs",
		InputSchema: llm.EmptySchema(),
		Run:         b.clearConsoleLogsRun,
	}
}

func (b *BrowseTools) clearConsoleLogsRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input clearConsoleLogsInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	// Ensure browser is initialized
	_, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	// Clear console logs with mutex protection
	b.consoleLogsMutex.Lock()
	logCount := len(b.consoleLogs)
	b.consoleLogs = make([]*runtime.EventConsoleAPICalled, 0)
	b.consoleLogsMutex.Unlock()

	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Cleared %d console log entries.", logCount))}
}
