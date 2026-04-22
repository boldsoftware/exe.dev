package llmgateway

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/stage"
	sloghttp "github.com/samber/slog-http"
	"github.com/stretchr/testify/require"
)

// leakedHeaderProbe returns a list of response header names that must NEVER
// appear in a gateway response returned to a tenant VM. Case-insensitive;
// matched against Header.Get (which canonicalizes names).
var leakedHeaderProbe = []string{
	// Anthropic
	"Anthropic-Organization-Id",
	"Anthropic-Ratelimit-Requests-Remaining",
	"Anthropic-Ratelimit-Tokens-Remaining",
	"Anthropic-Ratelimit-Input-Tokens-Remaining",
	"Anthropic-Ratelimit-Output-Tokens-Remaining",
	// OpenAI
	"Openai-Organization",
	"Openai-Project",
	"X-Ratelimit-Remaining-Requests",
	"X-Ratelimit-Remaining-Tokens",
	// Fireworks
	"Fireworks-Server-Processing-Time",
	"Fireworks-Prompt-Tokens",
	"X-Envoy-Upstream-Service-Time",
	"Server",
	// Generic upstream junk
	"Set-Cookie",
}

func newStripTestGateway(t *testing.T, keys APIKeys) http.Handler {
	t.Helper()
	db := newDB(t)
	setupTestBox(t, db, "test-box")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	gw := &llmGateway{
		now:       time.Now,
		data:      &DBGatewayData{db},
		apiKeys:   keys,
		env:       stage.Test(),
		log:       logger,
		creditMgr: NewCreditManager(&DBGatewayData{db}),
	}
	return sloghttp.NewWithConfig(logger, sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
	})(gw)
}

func assertNoLeakedHeaders(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	for _, h := range leakedHeaderProbe {
		if v := rr.Header().Values(h); len(v) > 0 {
			t.Errorf("response leaked upstream header %q = %q", h, v)
		}
	}
	// Sanity: the response should actually have some headers (proving we hit
	// the upstream and didn't trivially pass because of an early error).
	require.NotEmpty(t, rr.Header().Get("Content-Type"),
		"missing Content-Type — probe did not reach upstream; body=%s", rr.Body.String())
}

func TestGateway_StripsUpstreamHeaders_Anthropic(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	h := newStripTestGateway(t, APIKeys{Anthropic: key})

	reqBody := `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assertNoLeakedHeaders(t, rr)
}

func TestGateway_StripsUpstreamHeaders_OpenAI(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	h := newStripTestGateway(t, APIKeys{OpenAI: key})

	reqBody := `{"model":"gpt-4o-mini","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/_/gateway/openai/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assertNoLeakedHeaders(t, rr)
}

func TestGateway_StripsUpstreamHeaders_Fireworks(t *testing.T) {
	key := os.Getenv("FIREWORKS_API_KEY")
	if key == "" {
		t.Skip("FIREWORKS_API_KEY not set")
	}
	h := newStripTestGateway(t, APIKeys{Fireworks: key})

	reqBody := `{"model":"accounts/fireworks/models/gpt-oss-20b","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/_/gateway/fireworks/inference/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	assertNoLeakedHeaders(t, rr)
}

// TestStripUpstreamHeaders_Unit verifies the pure function without any
// network dependency.
func TestStripUpstreamHeaders_Unit(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Anthropic-Organization-Id", "3c473a21-dead-beef")
	h.Set("Openai-Organization", "bold-xw5gex")
	h.Set("Openai-Project", "proj_H7VhCOkNifA5BAy9LBStygE6")
	h.Add("Set-Cookie", "__cf_bm=secret; path=/")
	h.Set("X-Ratelimit-Remaining-Tokens", "12345")
	h.Set("Anthropic-Ratelimit-Requests-Remaining", "42")

	stripUpstreamHeaders(h)

	for _, name := range leakedHeaderProbe {
		require.Empty(t, h.Values(name), "%s should be stripped", name)
	}
	// Non-sensitive headers must remain.
	require.Equal(t, "application/json", h.Get("Content-Type"))
}
