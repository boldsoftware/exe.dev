package execore

import (
	"path/filepath"
	"strings"
	"testing"

	"exe.dev/billing"
	"exe.dev/billing/stripetest"
	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/prometheus/client_golang/prometheus"
)

func TestNewServerBootstrapStripeCatalog(t *testing.T) {
	t.Parallel()
	env := stage.Test()
	env.BootstrapStripeCatalog = true

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	s, err := NewServer(ServerConfig{
		Logger:             tslog.Slogger(t),
		HTTPAddr:           ":0",
		HTTPSAddr:          ":0",
		SSHAddr:            ":0",
		PluginAddr:         ":0",
		ExeproxServicePort: 0,
		DBPath:             dbPath,
		FakeEmailServer:    "",
		PiperdPort:         2222,
		GHWhoAmIPath:       "",
		ExeletAddresses:    nil,
		Env:                env,
		Billing: &billing.Manager{
			Client: stripetest.Record(t, filepath.Join(
				"testdata", "stripe", strings.ReplaceAll(t.Name(), "/", "_")+".httprr",
			)),
		},
		MetricsRegistry: prometheus.NewRegistry(),
		LMTPSocketPath:  "",
		MetricsdURL:     "",
		DashboardUI:     nil,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
}

func TestNewServerSkipsBootstrapStripeCatalog(t *testing.T) {
	t.Parallel()
	env := stage.Test()
	env.BootstrapStripeCatalog = false

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	s, err := NewServer(ServerConfig{
		Logger:             tslog.Slogger(t),
		HTTPAddr:           ":0",
		HTTPSAddr:          ":0",
		SSHAddr:            ":0",
		PluginAddr:         ":0",
		ExeproxServicePort: 0,
		DBPath:             dbPath,
		FakeEmailServer:    "",
		PiperdPort:         2222,
		GHWhoAmIPath:       "",
		ExeletAddresses:    nil,
		Env:                env,
		Billing: &billing.Manager{
			Client: stripetest.Record(t, filepath.Join(
				"testdata", "stripe", strings.ReplaceAll(t.Name(), "/", "_")+".httprr",
			)),
		},
		MetricsRegistry: prometheus.NewRegistry(),
		LMTPSocketPath:  "",
		MetricsdURL:     "",
		DashboardUI:     nil,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
}
