package gemini

import (
	"context"
	"os"
	"testing"
)

func TestGenerateContent(t *testing.T) {
	// TODO replace with local replay endpoint
	m := Model{
		Model:  "models/gemini-1.5-flash",
		APIKey: os.Getenv("GEMINI_API_KEY"),
	}
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}
	if m.APIKey == "" {
		t.Skip("skipping test without API key")
	}

	res, err := m.GenerateContent(context.Background(), &Request{
		Contents: []Content{{
			Parts: []Part{{
				Text: "What is the capital of France?",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("res: %+v", res)
}
