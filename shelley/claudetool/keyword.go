package claudetool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"sketch.dev/llm"
	"sketch.dev/llm/conversation"
)

// The Keyword tool provides keyword search.
// TODO: use an embedding model + re-ranker or otherwise do something nicer than this kludge.
// TODO: if we can get this fast enough, do it on the fly while the user is typing their prompt.
var Keyword = &llm.Tool{
	Name:        keywordName,
	Description: keywordDescription,
	InputSchema: llm.MustSchema(keywordInputSchema),
	Run:         keywordRun,
}

const (
	keywordName        = "keyword_search"
	keywordDescription = `
keyword_search locates files with a search-and-filter approach.
Use when navigating unfamiliar codebases with only conceptual understanding or vague user questions.

Effective use:
- Provide a detailed query for accurate relevance ranking
- Prefer MANY SPECIFIC terms over FEW GENERAL ones (high precision beats high recall)
- Order search terms by importance (most important first)
- Supports regex search terms for flexible matching

IMPORTANT: Do NOT use this tool if you have precise information like log lines, error messages, stack traces, filenames, or symbols. Use direct approaches (rg, cat, etc.) instead.
`

	// If you modify this, update the termui template for prettier rendering.
	keywordInputSchema = `
{
  "type": "object",
  "required": [
    "query",
    "search_terms"
  ],
  "properties": {
    "query": {
      "type": "string",
      "description": "A detailed statement of what you're trying to find or learn."
    },
    "search_terms": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "List of search terms in descending order of importance."
    }
  }
}
`
)

type keywordInput struct {
	Query       string   `json:"query"`
	SearchTerms []string `json:"search_terms"`
}

//go:embed keyword_system_prompt.txt
var keywordSystemPrompt string

// FindRepoRoot attempts to find the git repository root from the current directory
func FindRepoRoot(wd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = wd
	out, err := cmd.Output()
	// todo: cwd here and throughout
	if err != nil {
		return "", fmt.Errorf("failed to find git repository root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func keywordRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	var input keywordInput
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorToolOut(err)
	}
	wd := WorkingDir(ctx)
	root, err := FindRepoRoot(wd)
	if err == nil {
		wd = root
	}
	slog.InfoContext(ctx, "keyword search input", "query", input.Query, "keywords", input.SearchTerms, "wd", wd)

	// first remove stopwords
	var keep []string
	for _, term := range input.SearchTerms {
		out, err := ripgrep(ctx, wd, []string{term})
		if err != nil {
			return llm.ErrorToolOut(err)
		}
		if len(out) > 64*1024 {
			slog.InfoContext(ctx, "keyword search result too large", "term", term, "bytes", len(out))
			continue
		}
		keep = append(keep, term)
	}

	if len(keep) == 0 {
		return llm.ToolOut{LLMContent: llm.TextContent("each of those search terms yielded too many results")}
	}

	// peel off keywords until we get a result that fits in the query window
	var out string
	for {
		var err error
		out, err = ripgrep(ctx, wd, keep)
		if err != nil {
			return llm.ErrorToolOut(err)
		}
		if len(out) < 128*1024 {
			break
		}
		keep = keep[:len(keep)-1]
	}

	info := conversation.ToolCallInfoFromContext(ctx)
	convo := info.Convo.SubConvo()
	convo.SystemPrompt = strings.TrimSpace(keywordSystemPrompt)
	convo.PromptCaching = false

	initialMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			llm.StringContent("<pwd>\n" + wd + "\n</pwd>"),
			llm.StringContent("<ripgrep_results>\n" + out + "\n</ripgrep_results>"),
			llm.StringContent("<query>\n" + input.Query + "\n</query>"),
		},
	}

	resp, err := convo.SendMessage(initialMessage)
	if err != nil {
		return llm.ErrorfToolOut("failed to send relevance filtering message: %w", err)
	}
	if len(resp.Content) != 1 {
		return llm.ErrorfToolOut("unexpected number of messages (%d) in relevance filtering response: %v", len(resp.Content), resp.Content)
	}

	filtered := resp.Content[0].Text

	slog.InfoContext(ctx, "keyword search results processed",
		"bytes", len(out),
		"lines", strings.Count(out, "\n"),
		"files", strings.Count(out, "\n\n"),
		"query", input.Query,
		"filtered", filtered,
	)

	return llm.ToolOut{LLMContent: llm.TextContent(resp.Content[0].Text)}
}

func ripgrep(ctx context.Context, wd string, terms []string) (string, error) {
	args := []string{"-C", "10", "-i", "--line-number", "--with-filename"}
	for _, term := range terms {
		args = append(args, "-e", term)
	}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		// ripgrep returns exit code 1 when no matches are found, which is not an error for us
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "no matches found", nil
		}
		return "", fmt.Errorf("search failed: %v\n%s", err, out)
	}
	outStr := string(out)
	return outStr, nil
}
