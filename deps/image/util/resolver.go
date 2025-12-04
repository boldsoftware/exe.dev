package util

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/distribution/reference"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	"github.com/docker/docker/pkg/homedir"
)

var (
	configDir     = os.Getenv("DOCKER_CONFIG")
	configFileDir = ".docker"
	registryHost  docker.RegistryHost
)

// retryTransport wraps an http.RoundTripper and retries transient errors
// with exponential backoff up to 30 seconds total.
type retryTransport struct {
	base http.RoundTripper
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	const maxTotalTime = 30 * time.Second
	const initialBackoff = 100 * time.Millisecond

	// Determine if we can retry this request.
	// We can only retry if we can reset the body. For large streaming bodies
	// (like layer pushes), we cannot buffer them, so we don't retry.
	canRetry := req.Body == nil || req.GetBody != nil

	if !canRetry {
		// No way to replay the body, just do a single attempt
		return t.base.RoundTrip(req)
	}

	deadline := time.Now().Add(maxTotalTime)
	backoff := initialBackoff

	for {
		resp, err := t.base.RoundTrip(req)

		// Check if we should retry
		shouldRetry := false
		var retryAfter time.Duration
		if err != nil {
			// Network errors are retryable
			shouldRetry = true
		} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			// 429 and 5xx are retryable
			shouldRetry = true
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain and close the body so the connection can be reused
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if !shouldRetry {
			return resp, err
		}

		// Determine wait duration: use Retry-After if provided, otherwise exponential backoff with jitter
		var wait time.Duration
		if retryAfter > 0 {
			wait = retryAfter
		} else {
			// Add jitter: 0.5x to 1.5x the backoff
			wait = time.Duration(float64(backoff) * (0.5 + rand.Float64()))
			// Exponential backoff for next iteration
			backoff *= 2
		}

		// Check if we have time for another attempt
		if time.Now().Add(wait).After(deadline) {
			// No time left, return the error or response
			if err != nil {
				return nil, err
			}
			// Re-do the request to get a fresh response body
			if req.GetBody != nil {
				req.Body, _ = req.GetBody()
			}
			return t.base.RoundTrip(req)
		}

		// Check context before sleeping
		if req.Context().Err() != nil {
			if err != nil {
				return nil, err
			}
			return nil, req.Context().Err()
		}

		slog.Debug("retrying registry request", "wait", wait, "url", req.URL.String())

		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			if err != nil {
				return nil, err
			}
			return nil, req.Context().Err()
		}

		// Reset body for next attempt
		if req.GetBody != nil {
			req.Body, _ = req.GetBody()
		}
	}
}

// parseRetryAfter parses the Retry-After header value.
// It supports both delay-seconds (e.g., "120") and HTTP-date formats.
// Returns 0 if the header is empty or malformed.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}

	// Try parsing as seconds first (most common for rate limiting)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date (RFC 7231)
	if t, err := http.ParseTime(value); err == nil {
		duration := time.Until(t)
		if duration > 0 {
			return duration
		}
	}

	return 0
}

func CreateRegistryHost(imageRef reference.Named, username, password string, insecure, plainHTTP bool, dockerConfigPath string, pushOp bool) error {
	hostname, _ := splitHostname(imageRef.String())
	if hostname == "docker.io" {
		hostname = "registry-1.docker.io"
	}
	registryHost = docker.RegistryHost{
		Host:         hostname,
		Scheme:       "https",
		Path:         "/v2",
		Capabilities: docker.HostCapabilityPull | docker.HostCapabilityResolve,
	}
	if pushOp {
		registryHost.Capabilities |= docker.HostCapabilityPush
	}

	var base http.RoundTripper = http.DefaultTransport
	if insecure {
		base = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	}
	client := &http.Client{
		Transport: &retryTransport{base: base},
	}
	registryHost.Client = client

	if plainHTTP {
		registryHost.Scheme = "http"
	}

	credFunc := func(hostName string) (string, string, error) {
		if username != "" || password != "" {
			return username, password, nil
		}
		var (
			err error
			cfg *configfile.ConfigFile
		)
		if dockerConfigPath == "" || dockerConfigPath == configDir {
			cfg, err = config.Load(configDir)
			if err != nil {
				slog.Warn("unable to load default Docker auth config", "err", err)
			}
		} else {
			cfg = configfile.New(dockerConfigPath)
			if _, err := os.Stat(dockerConfigPath); err == nil {
				file, err := os.Open(dockerConfigPath)
				if err != nil {
					return "", "", fmt.Errorf("can't load docker config file %s: %w", dockerConfigPath, err)
				}
				defer file.Close()
				if err := cfg.LoadFromReader(file); err != nil {
					return "", "", fmt.Errorf("can't read and parse docker config file %s: %v", dockerConfigPath, err)
				}
			} else if !os.IsNotExist(err) {
				return "", "", fmt.Errorf("unable to open docker config file %s: %v", dockerConfigPath, err)
			}
		}
		if !cfg.ContainsAuth() {
			cfg.CredentialsStore = credentials.DetectDefaultStore(cfg.CredentialsStore)
		}
		hostname := resolveHostname(hostName)
		auth, err := cfg.GetAuthConfig(hostname)
		if err != nil {
			return "", "", err
		}
		if auth.IdentityToken != "" {
			return "", auth.IdentityToken, nil
		}
		return auth.Username, auth.Password, nil
	}
	registryHost.Authorizer = docker.NewDockerAuthorizer(docker.WithAuthCreds(credFunc))

	return nil
}

func GetResolver() remotes.Resolver {
	opts := docker.ResolverOptions{
		Hosts: getHosts,
	}
	return docker.NewResolver(opts)
}

func getHosts(name string) ([]docker.RegistryHost, error) {
	return []docker.RegistryHost{registryHost}, nil
}

// resolveHostname resolves Docker specific hostnames
func resolveHostname(hostname string) string {
	if strings.HasSuffix(hostname, "docker.io") {
		// Docker's `config.json` uses index.docker.io as the reference
		return LegacyDefaultHostname
	}
	return hostname
}

func init() {
	if configDir == "" {
		configDir = filepath.Join(homedir.Get(), configFileDir)
	}
}

func ConfigDir() string {
	return configDir
}
