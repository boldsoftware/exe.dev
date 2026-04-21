//go:build !linux

package netns

import (
	"context"
	"errors"
	"log/slog"

	api "exe.dev/pkg/api/exe/compute/v1"
)

var errNotSupported = errors.New("netns network manager is only supported on Linux")

type Manager struct{}

type Config struct {
	Bridge  string
	Network string
	Router  string
}

func NewManager(addr string, log *slog.Logger) (*Manager, error) {
	return nil, errNotSupported
}

func (m *Manager) Start(ctx context.Context) error { return errNotSupported }
func (m *Manager) Stop(ctx context.Context) error  { return errNotSupported }
func (m *Manager) Config(_ context.Context) any    { return nil }
func (m *Manager) Close() error                    { return errNotSupported }

func (m *Manager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, errNotSupported
}

func (m *Manager) DeleteInterface(ctx context.Context, id, ip, mac string) error {
	return errNotSupported
}

func (m *Manager) ApplyConnectionLimit(ctx context.Context, inst *api.Instance) error {
	return errNotSupported
}

func (m *Manager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return errNotSupported
}

func (m *Manager) ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error) {
	return nil, errNotSupported
}

func (m *Manager) GetInstanceByExtIP(ip string) (string, bool) {
	return "", false
}

func NsName(id string) string     { return "" }
func BridgeName(id string) string { return "" }
