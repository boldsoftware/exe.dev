package execore

import (
	"testing"

	"exe.dev/stage"
)

func TestProxyHostnameParsing(t *testing.T) {
	t.Parallel()

	prodServer := &Server{env: stage.Prod()}
	testServer := &Server{env: stage.Test()}

	tests := []struct {
		name        string
		server      *Server
		hostname    string
		expectedBox string
	}{
		{"prod valid exe.dev", prodServer, "test-box.exe.dev", "test-box"},
		{"prod rejects localhost", prodServer, "web.localhost", ""},
		{"prod valid simple", prodServer, "empty.exe.dev", "empty"},
		{"prod invalid domain", prodServer, "invalid.domain.com", ""},
		{"dev valid localhost", testServer, "dev-box.localhost", "dev-box"},
		{"dev rejects exe.dev", testServer, "dev-box.exe.dev", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.server.parseProxyHostname(test.hostname)
			if result != test.expectedBox {
				t.Fatalf("parseProxyHostname(%q) = %q, want %q", test.hostname, result, test.expectedBox)
			}
		})
	}
}
