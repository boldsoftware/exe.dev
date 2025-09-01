package container

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildArgs_WithExetini_ImageEntrypointAndCmd(t *testing.T) {
	got := buildEntrypointAndCmdArgs(true, "", []string{"docker-entrypoint.sh"}, []string{"node"}, false)
	want := []string{"-g", "--", "docker-entrypoint.sh", "node"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_WithExetini_NoEntrypointOrCmd(t *testing.T) {
	got := buildEntrypointAndCmdArgs(true, "", nil, nil, false)
	want := []string{"-g", "--", "sleep", "infinity"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_NoExetini_WithOverride(t *testing.T) {
	got := buildEntrypointAndCmdArgs(false, "echo hi", nil, nil, false)
	want := []string{"echo", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_NoExetini_NoneOrExeuntu(t *testing.T) {
	got1 := buildEntrypointAndCmdArgs(false, "none", nil, nil, false)
	got2 := buildEntrypointAndCmdArgs(false, "", nil, nil, true)
	want := []string{"sleep", "infinity"}
	if !reflect.DeepEqual(got1, want) {
		t.Fatalf("none override: got %v, want %v", got1, want)
	}
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("exeuntu: got %v, want %v", got2, want)
	}
}

func TestParseImageInspectJSON_Array(t *testing.T) {
	// Simulate nerdctl image inspect output (array of objects)
	payload := []map[string]any{
		{
			"Config": map[string]any{
				"Entrypoint": []string{"docker-entrypoint.sh"},
				"Cmd":        []string{"node"},
				"User":       "node",
			},
		},
	}
	data, _ := json.Marshal(payload)
	cfg, err := parseImageInspectJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.Entrypoint, []string{"docker-entrypoint.sh"}) {
		t.Fatalf("entrypoint mismatch: %v", cfg.Entrypoint)
	}
	if !reflect.DeepEqual(cfg.Cmd, []string{"node"}) {
		t.Fatalf("cmd mismatch: %v", cfg.Cmd)
	}
	if cfg.User != "node" {
		t.Fatalf("user mismatch: %s", cfg.User)
	}
}

func TestParseImageInspectJSON_SingleObject(t *testing.T) {
	payload := map[string]any{
		"Config": map[string]any{
			"Entrypoint": []string{"/bin/start"},
			"Cmd":        []string{"--serve"},
			"User":       "root",
		},
	}
	data, _ := json.Marshal(payload)
	cfg, err := parseImageInspectJSON(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.Entrypoint, []string{"/bin/start"}) {
		t.Fatalf("entrypoint mismatch: %v", cfg.Entrypoint)
	}
	if !reflect.DeepEqual(cfg.Cmd, []string{"--serve"}) {
		t.Fatalf("cmd mismatch: %v", cfg.Cmd)
	}
	if cfg.User != "root" {
		t.Fatalf("user mismatch: %s", cfg.User)
	}
}
