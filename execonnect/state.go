package execonnect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/boldsoftware/exe.dev/execonnect/internal/atomicfile"

	"tailscale.com/types/key"
)

const (
	stateVersion = 3
	maxStateSize = 1 << 20
)

// Credentials authenticate one enrolled execonnect installation to exed.
type Credentials struct {
	// ExternalConnectionID identifies the enrolled External Connection.
	ExternalConnectionID string
	// ConnectorSecret authenticates the connector to exed.
	ConnectorSecret string
}

// State is the complete durable local state owned by the execonnect daemon.
type State struct {
	// Version identifies the persisted state schema.
	Version int `json:"version"`
	// Enrolled reports whether the enrollment fields contain an active identity.
	Enrolled bool `json:"enrolled"`
	// ExternalConnectionID identifies the enrolled External Connection.
	ExternalConnectionID string `json:"external_connection_id,omitempty"`
	// ConnectorSecret authenticates the connector to exed.
	ConnectorSecret string `json:"connector_secret,omitempty"`
	// WireGuardPrivateKey is the connector's durable tunnel identity.
	WireGuardPrivateKey key.NodePrivate `json:"wireguard_private_key,omitempty"`
	// ExedURL is the canonical exed URL selected during enrollment.
	ExedURL string `json:"exed_url,omitempty"`
	// Endpoints is the complete pending or published endpoint configuration.
	Endpoints []endpointRegistryRecord `json:"endpoints"`
}

func newUnenrolledState() State {
	return State{Version: stateVersion, Endpoints: []endpointRegistryRecord{}}
}

// Credentials returns the authentication fields from state.
func (state State) Credentials() Credentials {
	return Credentials{
		ExternalConnectionID: state.ExternalConnectionID,
		ConnectorSecret:      state.ConnectorSecret,
	}
}

func (state State) endpointSnapshot() (EndpointRegistrySnapshot, error) {
	endpoints, err := endpointsFromRecords(state.Endpoints)
	if err != nil {
		return EndpointRegistrySnapshot{}, err
	}
	return EndpointRegistrySnapshot{Endpoints: endpoints}, nil
}

func (state State) withEndpointSnapshot(snapshot EndpointRegistrySnapshot) (State, error) {
	records, err := recordsFromEndpoints(snapshot.Endpoints)
	if err != nil {
		return State{}, err
	}
	state.Endpoints = records
	return state, nil
}

// LoadState loads and validates an existing daemon state file.
func LoadState(path string) (State, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return State{}, fmt.Errorf("empty state path")
	}
	file, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return State{}, fmt.Errorf("stat state: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return State{}, fmt.Errorf("state file %q must not be accessible by group or others", path)
	}
	if info.Size() > maxStateSize {
		return State{}, fmt.Errorf("state file is too large")
	}

	var state State
	decoder := json.NewDecoder(io.LimitReader(file, maxStateSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return State{}, fmt.Errorf("state file must contain one JSON document")
	} else if err != io.EOF {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	if err := state.validate(); err != nil {
		return State{}, fmt.Errorf("invalid state: %w", err)
	}
	return state, nil
}

func loadOrCreateState(path string) (State, error) {
	state, err := LoadState(path)
	if err == nil {
		return state, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return newUnenrolledState(), nil
	}
	return State{}, err
}

func (state State) validate() error {
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported state version %d", state.Version)
	}
	if state.Endpoints == nil {
		return fmt.Errorf("endpoints array is required")
	}
	if _, err := endpointsFromRecords(state.Endpoints); err != nil {
		return err
	}
	if !state.Enrolled {
		if state.ExternalConnectionID != "" || state.ConnectorSecret != "" || !state.WireGuardPrivateKey.IsZero() || state.ExedURL != "" {
			return fmt.Errorf("unenrolled state contains enrollment identity")
		}
		return nil
	}
	if err := state.Credentials().validate(); err != nil {
		return err
	}
	if state.WireGuardPrivateKey.IsZero() {
		return fmt.Errorf("WireGuard private key is required")
	}
	normalizedExedURL, err := NormalizeExedURL(state.ExedURL)
	if err != nil {
		return err
	}
	if normalizedExedURL != state.ExedURL {
		return fmt.Errorf("exed URL is not canonical")
	}
	return nil
}

func (credentials Credentials) validate() error {
	if credentials.ExternalConnectionID == "" || credentials.ExternalConnectionID != strings.TrimSpace(credentials.ExternalConnectionID) {
		return fmt.Errorf("external connection ID is required")
	}
	if credentials.ConnectorSecret == "" || credentials.ConnectorSecret != strings.TrimSpace(credentials.ConnectorSecret) {
		return fmt.Errorf("connector secret is required")
	}
	return nil
}

func writeState(path string, state State) error {
	if err := state.validate(); err != nil {
		return fmt.Errorf("invalid state: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	if err := ensureStateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := atomicfile.WriteFileSync(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

func ensureStateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure state directory: %w", err)
	}
	return nil
}
