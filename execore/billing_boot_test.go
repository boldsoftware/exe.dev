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

func TestNewServerInstallPricesWhenBillingEnabled(t *testing.T) {
	env := stage.Test()
	env.SkipBilling = false

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
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
}

func TestNewServerSkipsInstallPricesWhenBillingDisabled(t *testing.T) {
	env := stage.Test()
	env.SkipBilling = true

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
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
}
