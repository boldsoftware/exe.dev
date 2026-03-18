package exed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   []string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "no flags gives local default",
			flags: nil,
			want:  map[string]string{"local": defaultLocalBase},
		},
		{
			name:  "single env excludes local",
			flags: []string{"staging:https://exed-staging.example.com"},
			want: map[string]string{
				"staging": "https://exed-staging.example.com",
			},
		},
		{
			name: "multiple envs excludes local",
			flags: []string{
				"staging:https://staging.example.com",
				"prod:https://prod.example.com",
			},
			want: map[string]string{
				"staging": "https://staging.example.com",
				"prod":    "https://prod.example.com",
			},
		},
		{
			name:  "override local",
			flags: []string{"local:http://other:9090"},
			want:  map[string]string{"local": "http://other:9090"},
		},
		{
			name:  "trailing slash stripped",
			flags: []string{"prod:https://exed.example.com/"},
			want:  map[string]string{"prod": "https://exed.example.com"},
		},
		{
			name:    "missing url",
			flags:   []string{"staging:"},
			wantErr: true,
		},
		{
			name:    "missing env name",
			flags:   []string{":https://foo"},
			wantErr: true,
		},
		{
			name:    "no colon",
			flags:   []string{"staging"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.Envs) != len(tt.want) {
				t.Fatalf("got %d envs, want %d", len(cfg.Envs), len(tt.want))
			}
			for k, v := range tt.want {
				if cfg.Envs[k] != v {
					t.Errorf("env %q: got %q, want %q", k, cfg.Envs[k], v)
				}
			}
		})
	}
}

const sampleExedResponse = `[
  {
    "address": "tcp://exe-ctr-03:9080",
    "version": "exe/2275ca7",
    "available": true,
    "status": "healthy",
    "instance_count": 399,
    "instance_limit": 400,
    "load_average": "11.24",
    "mem_free": "11.8",
    "swap_free": "2502.5",
    "disk_free": "995.3",
    "rx_rate": "2.7",
    "tx_rate": "2.7",
    "debug_url": "http://exe-ctr-03:9081",
    "cgtop_url": "http://exe-ctr-03:9090"
  },
  {
    "address": "tcp://exelet-lax-prod-01:9080",
    "version": "exe/abcdef0",
    "available": true,
    "status": "healthy",
    "instance_count": 601,
    "instance_limit": 800
  }
]`

func TestHostnameFromAddress(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"tcp://exe-ctr-03:9080", "exe-ctr-03"},
		{"tcp://exelet-lax-prod-01:9080", "exelet-lax-prod-01"},
		{"tcp://host:1234", "host"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		got := hostnameFromAddress(tt.addr)
		if got != tt.want {
			t.Errorf("hostnameFromAddress(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestParseExelets(t *testing.T) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(sampleExedResponse), &raw); err != nil {
		t.Fatalf("unmarshal sample: %v", err)
	}

	env := EnvExelets{Env: "prod", Exelets: raw}
	parsed := env.ParseExelets()

	if len(parsed) != 2 {
		t.Fatalf("got %d exelets, want 2", len(parsed))
	}

	if parsed[0].Hostname != "exe-ctr-03" {
		t.Errorf("hostname[0]: got %q, want %q", parsed[0].Hostname, "exe-ctr-03")
	}
	if parsed[0].Instances != 399 || parsed[0].Capacity != 400 {
		t.Errorf("capacity[0]: got %d/%d, want 399/400", parsed[0].Instances, parsed[0].Capacity)
	}

	if parsed[1].Hostname != "exelet-lax-prod-01" {
		t.Errorf("hostname[1]: got %q, want %q", parsed[1].Hostname, "exelet-lax-prod-01")
	}
	if parsed[1].Instances != 601 || parsed[1].Capacity != 800 {
		t.Errorf("capacity[1]: got %d/%d, want 601/800", parsed[1].Instances, parsed[1].Capacity)
	}
}

func TestFetchAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/exelets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleExedResponse))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Envs: map[string]string{"test": srv.URL}}
	client := NewClient(cfg)

	results := client.FetchAll(context.Background())
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Env != "test" {
		t.Errorf("env: got %q, want %q", r.Env, "test")
	}
	if r.Error != "" {
		t.Errorf("unexpected error: %s", r.Error)
	}
	if len(r.Exelets) != 2 {
		t.Fatalf("got %d exelets, want 2", len(r.Exelets))
	}

	parsed := r.ParseExelets()
	if len(parsed) != 2 {
		t.Fatalf("parsed %d exelets, want 2", len(parsed))
	}
	if parsed[0].Hostname != "exe-ctr-03" {
		t.Errorf("parsed hostname: got %q, want %q", parsed[0].Hostname, "exe-ctr-03")
	}
}

func TestFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/exelets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sampleExedResponse))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Envs: map[string]string{"staging": srv.URL}}
	client := NewClient(cfg)

	result, err := client.Fetch(context.Background(), "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Exelets) != 2 {
		t.Fatalf("got %d exelets, want 2", len(result.Exelets))
	}
}

func TestFetchUnknownEnv(t *testing.T) {
	cfg := &Config{Envs: map[string]string{}}
	client := NewClient(cfg)

	_, err := client.Fetch(context.Background(), "unknown")
	if err == nil {
		t.Fatal("expected error for unknown env")
	}
}

func TestFetchHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/exelets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &Config{Envs: map[string]string{"broken": srv.URL}}
	client := NewClient(cfg)

	results := client.FetchAll(context.Background())
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected error for 500 response")
	}
}
