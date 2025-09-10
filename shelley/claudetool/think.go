package claudetool

import (
	"context"
	"encoding/json"

	"shelley.exe.dev/llm"
)

// The Think tool provides space to think.
var Think = &llm.Tool{
	Name:        thinkName,
	Description: thinkDescription,
	InputSchema: llm.MustSchema(thinkInputSchema),
	Run:         thinkRun,
}

const (
	thinkName        = "think"
	thinkDescription = `Think out loud, take notes, form plans. Has no external effects.`

	// If you modify this, update the termui template for prettier rendering.
	thinkInputSchema = `
{
  "type": "object",
  "required": ["thoughts"],
  "properties": {
    "thoughts": {
      "type": "string",
      "description": "The thoughts, notes, or plans to record"
    }
  }
}
`
)

func thinkRun(ctx context.Context, m json.RawMessage) llm.ToolOut {
	return llm.ToolOut{LLMContent: llm.TextContent("recorded")}
}
