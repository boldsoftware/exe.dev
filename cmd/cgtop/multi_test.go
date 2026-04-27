//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"http://exelet-01:9090", "exelet-01"},
		{"http://10.0.0.5:9090", "10.0.0.5"},
		{"https://my-host.example.com:9090/", "my-host.example.com"},
		{"http://localhost", "localhost"},
		{"", ""},
	}
	for _, tt := range tests {
		got := nameFromURL(tt.url)
		if got != tt.want {
			t.Errorf("nameFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestFetchHosts(t *testing.T) {
	cmd := `printf 'http://host-a:9090\nhttp://host-b:9090\n'`
	mc := newMultiCollector(cmd)
	hosts, err := mc.fetchHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	if hosts[0].Name != "host-a" {
		t.Errorf("hosts[0].Name = %q, want host-a", hosts[0].Name)
	}
	if hosts[0].URL != "http://host-a:9090" {
		t.Errorf("hosts[0].URL = %q, want http://host-a:9090", hosts[0].URL)
	}
	if hosts[1].Name != "host-b" {
		t.Errorf("hosts[1].Name = %q, want host-b", hosts[1].Name)
	}
}

func TestFetchHosts_Cached(t *testing.T) {
	countFile := t.TempDir() + "/count"
	cmd := `echo "http://h:9090" && echo x >> ` + countFile
	mc := newMultiCollector(cmd)
	mc.fetchHosts(t.Context())
	mc.fetchHosts(t.Context())
	mc.fetchHosts(t.Context())

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(data), "x")
	if lines != 1 {
		t.Errorf("expected 1 command execution (cached), got %d", lines)
	}
}

func TestFetchHosts_EmptyLines(t *testing.T) {
	cmd := `printf 'http://h1:9090\n\n  \nhttp://h2:9090\n'`
	mc := newMultiCollector(cmd)
	hosts, err := mc.fetchHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts (blank lines skipped), got %d", len(hosts))
	}
}

func TestFetchHosts_CommandError(t *testing.T) {
	mc := newMultiCollector("exit 1")
	_, err := mc.fetchHosts(t.Context())
	if err == nil {
		t.Error("expected error from failing command")
	}
}

func TestFetchMultiData(t *testing.T) {
	cgtop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse{
			System: SystemStats{Hostname: "test-host"},
			Tree: &CgroupNode{
				Name:   "/",
				Path:   "",
				Stats:  map[string]float64{"pids.current": 42},
				Config: map[string]any{},
			},
			Timestamp: 1234567890,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer cgtop.Close()

	mc := newMultiCollector(`echo "` + cgtop.URL + `"`)
	hosts, err := mc.fetchHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	name := hosts[0].Name

	result := mc.fetchMultiData(t.Context(), []string{name}, "")
	hd, ok := result.Hosts[name]
	if !ok {
		t.Fatalf("%s not in result", name)
	}
	if hd.Error != "" {
		t.Fatalf("unexpected error: %s", hd.Error)
	}
	if hd.Data == nil {
		t.Fatal("expected data")
	}
	if hd.Data.System.Hostname != "test-host" {
		t.Errorf("hostname = %q, want test-host", hd.Data.System.Hostname)
	}
	if hd.Data.Tree.Stats["pids.current"] != 42 {
		t.Errorf("pids.current = %f, want 42", hd.Data.Tree.Stats["pids.current"])
	}
}

func TestFetchMultiData_UnknownHost(t *testing.T) {
	mc := newMultiCollector(`echo "http://h:9090"`)
	result := mc.fetchMultiData(t.Context(), []string{"nonexistent"}, "")

	hd := result.Hosts["nonexistent"]
	if hd == nil {
		t.Fatal("expected entry for nonexistent")
	}
	if hd.Error == "" {
		t.Error("expected error for unknown host")
	}
}

func TestFetchMultiData_WithRoot(t *testing.T) {
	var gotRoot string
	cgtop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRoot = r.URL.Query().Get("root")
		resp := APIResponse{
			Tree:      &CgroupNode{Name: "/", Path: "", Stats: map[string]float64{}, Config: map[string]any{}},
			Timestamp: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer cgtop.Close()

	mc := newMultiCollector(`echo "` + cgtop.URL + `"`)
	hosts, _ := mc.fetchHosts(t.Context())
	mc.fetchMultiData(t.Context(), []string{hosts[0].Name}, "system.slice")

	if gotRoot != "system.slice" {
		t.Errorf("root filter not passed through, got %q", gotRoot)
	}
}
