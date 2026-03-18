// Package exed provides a client for fetching exelet information from exed hosts.
package exed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultLocalBase = "http://localhost:8080"
	exeletsPath      = "/debug/exelets?format=json"
)

// Config maps environment names to exed base URLs.
type Config struct {
	Envs map[string]string // e.g. {"local": "http://localhost:8080", "staging": "https://exed-staging.example.com"}
}

// ParseFlags parses --exed flag values of the form "env:base-url" into a Config.
// The base URL should not include a path — the client appends /debug/exelets?format=json
// automatically. If no flags are provided, a config with only the "local" default is returned.
// If any flags are provided, only those environments are configured (local is
// not implicitly added, but can be explicitly included via --exed local:<url>).
func ParseFlags(flags []string) (*Config, error) {
	cfg := &Config{Envs: make(map[string]string)}
	if len(flags) == 0 {
		cfg.Envs["local"] = defaultLocalBase
		return cfg, nil
	}
	for _, f := range flags {
		// strings.Cut splits on the first ":" — env names don't contain ":",
		// so "staging:https://host" correctly yields env="staging", url="https://host".
		env, baseURL, ok := strings.Cut(f, ":")
		if !ok || env == "" || baseURL == "" {
			return nil, fmt.Errorf("invalid --exed flag %q: expected format env:base-url (e.g. prod:https://exed.example.com)", f)
		}
		cfg.Envs[env] = strings.TrimRight(baseURL, "/")
	}
	return cfg, nil
}

// Client fetches exelet data from configured exed hosts.
type Client struct {
	cfg    *Config
	client *http.Client
}

// NewClient creates a new exed client.
func NewClient(cfg *Config) *Client {
	return &Client{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// exeletJSON is the raw JSON shape returned by exed's /debug/exelets endpoint.
type exeletJSON struct {
	Address       string `json:"address"` // "tcp://exe-ctr-03:9080"
	InstanceCount int    `json:"instance_count"`
	InstanceLimit int    `json:"instance_limit"`
}

// ExeletInfo is the parsed, normalized form used by exe-ops.
type ExeletInfo struct {
	Hostname  string
	Instances int
	Capacity  int
}

// EnvExelets holds the exelet data for a single environment.
type EnvExelets struct {
	Env     string            `json:"env"`
	URL     string            `json:"url"`
	Exelets []json.RawMessage `json:"exelets,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// ParseExelets parses the raw exelet JSON into typed ExeletInfo structs.
func (e *EnvExelets) ParseExelets() []ExeletInfo {
	var result []ExeletInfo
	for _, raw := range e.Exelets {
		var ej exeletJSON
		if err := json.Unmarshal(raw, &ej); err != nil || ej.Address == "" {
			continue
		}
		hostname := hostnameFromAddress(ej.Address)
		if hostname == "" {
			continue
		}
		result = append(result, ExeletInfo{
			Hostname:  hostname,
			Instances: ej.InstanceCount,
			Capacity:  ej.InstanceLimit,
		})
	}
	return result
}

// hostnameFromAddress extracts the hostname from "tcp://host:port".
func hostnameFromAddress(addr string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// FetchAll fetches exelet data from all configured environments concurrently.
func (c *Client) FetchAll(ctx context.Context) []EnvExelets {
	var (
		mu      sync.Mutex
		results []EnvExelets
		wg      sync.WaitGroup
	)

	for env, url := range c.cfg.Envs {
		wg.Add(1)
		go func(env, url string) {
			defer wg.Done()
			result := c.fetch(ctx, env, url)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(env, url)
	}

	wg.Wait()
	return results
}

// Fetch fetches exelet data from a single environment.
func (c *Client) Fetch(ctx context.Context, env string) (EnvExelets, error) {
	url, ok := c.cfg.Envs[env]
	if !ok {
		return EnvExelets{}, fmt.Errorf("unknown environment %q", env)
	}
	result := c.fetch(ctx, env, url)
	if result.Error != "" {
		return result, fmt.Errorf("%s", result.Error)
	}
	return result, nil
}

// Envs returns the list of configured environment names.
func (c *Client) Envs() []string {
	envs := make([]string, 0, len(c.cfg.Envs))
	for env := range c.cfg.Envs {
		envs = append(envs, env)
	}
	return envs
}

func (c *Client) fetch(ctx context.Context, env, baseURL string) EnvExelets {
	fetchURL := baseURL + exeletsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return EnvExelets{Env: env, URL: baseURL, Error: fmt.Sprintf("create request: %v", err)}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return EnvExelets{Env: env, URL: baseURL, Error: fmt.Sprintf("fetch: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return EnvExelets{Env: env, URL: baseURL, Error: fmt.Sprintf("unexpected status: %d", resp.StatusCode)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return EnvExelets{Env: env, URL: baseURL, Error: fmt.Sprintf("read body: %v", err)}
	}

	var exelets []json.RawMessage
	if err := json.Unmarshal(body, &exelets); err != nil {
		return EnvExelets{Env: env, URL: baseURL, Error: fmt.Sprintf("decode JSON: %v", err)}
	}

	return EnvExelets{Env: env, URL: baseURL, Exelets: exelets}
}
