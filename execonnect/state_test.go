package execonnect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tailscale.com/types/key"
)

func TestStatePersistsCredentialsAndEndpointsAtomically(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	endpoint, err := NormalizeEndpoint("database", "tcp://db.internal:5432", "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := newUnenrolledState().withEndpointSnapshot(EndpointRegistrySnapshot{Endpoints: []Endpoint{endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	state.Enrolled = true
	state.ExternalConnectionID = "ec_one"
	state.ConnectorSecret = "ecs_one"
	state.WireGuardPrivateKey = key.NewNode()
	state.ExedURL = DefaultExedURL
	if err := writeState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Enrolled || loaded.ExternalConnectionID != "ec_one" || len(loaded.Endpoints) != 1 || loaded.Endpoints[0].Name != "database" {
		t.Fatalf("loaded state = %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(directory, "endpoints.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("separate endpoint registry exists: %v", err)
	}

	invalid := state
	invalid.ExternalConnectionID = ""
	if err := writeState(path, invalid); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	unchanged, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ExternalConnectionID != "ec_one" {
		t.Fatalf("failed replacement changed state: %#v", unchanged)
	}
}

func TestStateHasNoLegacyImportPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"version":2,"external_connection_id":"ec_old","connector_secret":"ecs_old","wireguard_private_key":"privkey:0000000000000000000000000000000000000000000000000000000000000000","exed_url":"https://exe.dev"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil || !strings.Contains(err.Error(), "unsupported state version 2") {
		t.Fatalf("LoadState legacy error = %v", err)
	}
}

func TestUnenrolledStateRejectsIdentity(t *testing.T) {
	state := newUnenrolledState()
	state.ConnectorSecret = "secret"
	if err := state.validate(); err == nil || !strings.Contains(err.Error(), "unenrolled state contains enrollment identity") {
		t.Fatalf("validate error = %v", err)
	}
}
