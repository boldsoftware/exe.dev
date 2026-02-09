package execore

import (
	"testing"
)

func TestResolveExelet(t *testing.T) {
	client16 := &exeletClient{}
	client18 := &exeletClient{}
	clientLax := &exeletClient{}
	s := &Server{
		exeletClients: map[string]*exeletClient{
			"tcp://exe-ctr-16:9080":          client16,
			"tcp://exe-ctr-18:9080":          client18,
			"tcp://exelet-lax2-prod-01:9080": clientLax,
		},
	}

	tests := []struct {
		name       string
		input      string
		wantAddr   string
		wantClient *exeletClient
	}{
		{
			name:       "full address",
			input:      "tcp://exe-ctr-16:9080",
			wantAddr:   "tcp://exe-ctr-16:9080",
			wantClient: client16,
		},
		{
			name:       "short hostname",
			input:      "exe-ctr-16",
			wantAddr:   "tcp://exe-ctr-16:9080",
			wantClient: client16,
		},
		{
			name:       "short hostname with dashes",
			input:      "exelet-lax2-prod-01",
			wantAddr:   "tcp://exelet-lax2-prod-01:9080",
			wantClient: clientLax,
		},
		{
			name:       "unknown hostname",
			input:      "unknown-host",
			wantAddr:   "",
			wantClient: nil,
		},
		{
			name:       "partial hostname should not match",
			input:      "exe-ctr-1",
			wantAddr:   "",
			wantClient: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAddr, gotClient := s.resolveExelet(tt.input)
			if gotAddr != tt.wantAddr {
				t.Errorf("resolveExelet(%q) addr = %q, want %q", tt.input, gotAddr, tt.wantAddr)
			}
			if gotClient != tt.wantClient {
				t.Errorf("resolveExelet(%q) client mismatch", tt.input)
			}
		})
	}
}

func TestResolveExeletAmbiguous(t *testing.T) {
	s := &Server{
		exeletClients: map[string]*exeletClient{
			"tcp://myhost:9080": {},
			"tcp://myhost:9081": {},
		},
	}

	addr, client := s.resolveExelet("myhost")
	if addr != "" || client != nil {
		t.Errorf("resolveExelet with ambiguous hostname = (%q, %v), want empty", addr, client)
	}
}

func TestExeletHostnames(t *testing.T) {
	s := &Server{
		exeletClients: map[string]*exeletClient{
			"tcp://exe-ctr-16:9080": {},
		},
	}

	// With no clients up, should be empty
	got := s.exeletHostnames()
	if len(got) != 0 {
		t.Errorf("exeletHostnames() with no up clients = %v, want []", got)
	}

	// Mark client as up
	s.exeletClients["tcp://exe-ctr-16:9080"].up.Store(true)
	got = s.exeletHostnames()
	if len(got) != 1 || got[0] != "exe-ctr-16" {
		t.Errorf("exeletHostnames() = %v, want [exe-ctr-16]", got)
	}
}
