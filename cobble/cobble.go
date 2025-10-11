// Package cobble provides a polished Pebble wrapper for testing against a
// local ACME server.
package cobble

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme"
)

//go:embed certs/*
var defaultCerts embed.FS

type Config struct {
	ListenAddress string // The address to listen on (default is 127.0.0.1:14000)
	Certificate   string // The path to the server certificate (default is certs/cert.pem)
	PrivateKey    string // The path to the server private key (default is certs/key.pem)
	HTTPPort      int    // The port for the HTTP-01 challenge server (default is 5002)

	AlwaysValid bool // Whether to treat all certificate requests as valid (default is true)

	Certs fs.FS     // The filesystem containing the cert.pem and key.pem files for the server (default is ./certs)
	Log   io.Writer // Where to write stdout and stdin logs (default is os.Stdout)
	Dir   string    // The directory to use for Pebble's database (default is a temp dir)
}

type Stone struct {
	stop   func() error
	client *acme.Client
}

func (s *Stone) Client() *acme.Client {
	return s.client
}

func (s *Stone) Stop() error {
	return s.stop()
}

// Start starts a Pebble ACME test server and returns an ACME client
// configured to trust it.
//
// It runs in a temporary directory with the files in config copied
// into it before starting.
// It assumes config.json file is present in the root of config.
func Start(ctx context.Context, cfg *Config) (*Stone, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ListenAddress = cmp.Or(cfg.ListenAddress, "127.0.0.1:14000")
	cfg.Certificate = cmp.Or(cfg.Certificate, "certs/cert.pem")
	cfg.PrivateKey = cmp.Or(cfg.PrivateKey, "certs/key.pem")
	cfg.HTTPPort = cmp.Or(cfg.HTTPPort, 5002)
	if !cfg.AlwaysValid {
		cfg.AlwaysValid = true
	}
	cfg.Certs = cmp.Or(cfg.Certs, fs.FS(defaultCerts))
	cfg.Log = cmp.Or(cfg.Log, io.Writer(os.Stdout))

	if cfg.Dir == "" {
		dir, err := os.MkdirTemp("", "cobble-pebble-")
		if err != nil {
			return nil, err
		}
		cfg.Dir = dir
	}

	if err := os.CopyFS(cfg.Dir, cfg.Certs); err != nil {
		return nil, err
	}

	if _, err := writeConfig(cfg.Dir, cfg); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	// The cancel is called in the stop function passed to Stone.

	buildCmd := exec.CommandContext(ctx, "go", "build",
		"-o", cfg.Dir,
		"github.com/letsencrypt/pebble/cmd/pebble",
	)
	logs, err := buildCmd.CombinedOutput()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%v: %w: %s", buildCmd, err, logs)
	}

	cmd := exec.CommandContext(ctx, filepath.Join(cfg.Dir, "pebble"),
		"-config", "config.json",
	)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(),
		// Tell Pebble to treat all certificate requests as valid, avoiding DNS lookups
		// and the need to run a DNS server during tests.
		//
		// This skips verifying that /.well-known/acme-challenge/ is correctly hooked up,
		// which is acceptable for now. If needed, this package could be split out and
		// tested more thoroughly later. For now, we rely on local and demo testing to
		// surface any real issues.
		"PEBBLE_VA_ALWAYS_VALID="+fmt.Sprint(cfg.AlwaysValid),
		// Disable artificial sleep delays in the validation path to run at full speed.
		"PEBBLE_VA_NOSLEEP=1",
	)
	cmd.Stdout = cfg.Log
	cmd.Stderr = cfg.Log

	fmt.Fprintf(cfg.Log, "Starting Pebble: %v\n", cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	// Configure the ACME client to trust Pebble's management server certificate
	// (from testdata/pebble/certs/cert.pem) which is used for the HTTPS endpoints
	certPool := x509.NewCertPool()
	pebbleCert, err := fs.ReadFile(cfg.Certs, "certs/cert.pem")
	if err != nil {
		cancel()
		return nil, err
	}
	if !certPool.AppendCertsFromPEM(pebbleCert) {
		cancel()
		return nil, errors.New("failed to append Pebble server certificate to cert pool")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: certPool,
			},
		},
	}

	// Poll until Pebble is ready by checking if the directory endpoint responds
	directoryURL := fmt.Sprintf("https://%s/dir", cfg.ListenAddress)
	start := time.Now()
	for {
		if time.Since(start) > 10*time.Second {
			cancel()
			return nil, errors.New("timed out waiting for Pebble to start")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", directoryURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Generate a new ECDSA key for the ACME client
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		cancel()
		return nil, err
	}

	stone := &Stone{
		client: &acme.Client{
			Key:          accountKey,
			DirectoryURL: fmt.Sprintf("https://%s/dir", cfg.ListenAddress),
			HTTPClient:   httpClient,
		},
		stop: func() error { cancel(); return cmd.Wait() },
	}
	return stone, nil
}

func writeConfig(dir string, cfg *Config) (path string, _ error) {
	// Pebble expects the config to be nested under a "pebble" key
	wrapped := map[string]any{
		"pebble": map[string]any{
			"listenAddress": cfg.ListenAddress,
			"certificate":   cfg.Certificate,
			"privateKey":    cfg.PrivateKey,
			"httpPort":      cfg.HTTPPort,
			"ca": map[string]any{
				"cert": cfg.Certificate,
				"key":  cfg.PrivateKey,
			},
		},
	}
	data, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return "", err
	}
	path = filepath.Join(dir, "config.json")
	return path, os.WriteFile(path, data, 0o644)
}
