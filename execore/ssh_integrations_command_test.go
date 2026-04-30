package execore

import (
	"encoding/json"
	"testing"
)

func TestRedactInjectedHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "empty", header: "", want: ""},
		{name: "custom", header: "X-Auth:secret123", want: "X-Auth:***"},
		{name: "authorization bearer", header: "Authorization:Bearer my-secret-token", want: "Authorization:***"},
		{name: "authorization basic", header: "Authorization:Basic dGVzdDp0b2tlbg==", want: "Authorization:***"},
		{name: "authorization no scheme separator", header: "Authorization:Bearer-token", want: "Authorization:***"},
		{name: "malformed", header: "totally-secret", want: "***"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := redactInjectedHeader(tt.header); got != tt.want {
				t.Fatalf("redactInjectedHeader(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestRedactedIntegrationConfigJSON(t *testing.T) {
	t.Parallel()

	raw := redactedIntegrationConfigJSON("http-proxy", `{"target":"https://testuser:testpass@example.com","header":"Authorization:Bearer my-secret-token"}`)

	var cfg httpProxyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal redacted config: %v", err)
	}
	if cfg.Target != "https://testuser:%2A%2A%2A@example.com" {
		t.Fatalf("target = %q, want %q", cfg.Target, "https://testuser:%2A%2A%2A@example.com")
	}
	if cfg.Header != "Authorization:***" {
		t.Fatalf("header = %q, want %q", cfg.Header, "Authorization:***")
	}
}
