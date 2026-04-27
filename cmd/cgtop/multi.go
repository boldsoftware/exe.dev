//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CgtopHost represents a remote cgtop instance discovered by the discover command.
type CgtopHost struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// MultiResponse wraps responses from multiple hosts.
type MultiResponse struct {
	Hosts map[string]*HostData `json:"hosts"` // keyed by name
}

// HostData is the data from one host's cgtop.
type HostData struct {
	Hostname string       `json:"hostname"`
	Data     *APIResponse `json:"data,omitempty"`
	Error    string       `json:"error,omitempty"`
}

type multiCollector struct {
	discoverCmd string
	client      *http.Client

	mu        sync.Mutex
	hosts     []CgtopHost
	lastFetch time.Time
}

func newMultiCollector(discoverCmd string) *multiCollector {
	return &multiCollector{
		discoverCmd: discoverCmd,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// nameFromURL extracts a short display name from a cgtop URL.
// e.g. "http://exelet-01:9090" → "exelet-01", "http://10.0.0.5:9090" → "10.0.0.5".
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	h := u.Hostname()
	if h == "" {
		return raw
	}
	return h
}

func (m *multiCollector) fetchHosts(ctx context.Context) ([]CgtopHost, error) {
	m.mu.Lock()
	if time.Since(m.lastFetch) < 30*time.Second && m.hosts != nil {
		hosts := m.hosts
		m.mu.Unlock()
		return hosts, nil
	}
	m.mu.Unlock()

	cmd := exec.CommandContext(ctx, "sh", "-c", m.discoverCmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running discover command: %w", err)
	}

	var hosts []CgtopHost
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		hosts = append(hosts, CgtopHost{
			Name: nameFromURL(line),
			URL:  line,
		})
	}

	m.mu.Lock()
	m.hosts = hosts
	m.lastFetch = time.Now()
	m.mu.Unlock()

	return hosts, nil
}

func (m *multiCollector) fetchMultiData(ctx context.Context, hostnames []string, rootFilter string) *MultiResponse {
	hosts, err := m.fetchHosts(ctx)
	hostMap := make(map[string]CgtopHost)
	if err == nil {
		for _, h := range hosts {
			hostMap[h.Name] = h
		}
	}

	result := &MultiResponse{
		Hosts: make(map[string]*HostData, len(hostnames)),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, hostname := range hostnames {
		wg.Add(1)
		go func(hostname string) {
			defer wg.Done()

			hd := &HostData{Hostname: hostname}

			host, ok := hostMap[hostname]
			if !ok {
				hd.Error = "host not found"
				mu.Lock()
				result.Hosts[hostname] = hd
				mu.Unlock()
				return
			}

			fetchURL := host.URL + "/api/data"
			if rootFilter != "" {
				fetchURL += "?root=" + rootFilter
			}
			reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fetchURL, nil)
			if err != nil {
				hd.Error = fmt.Sprintf("creating request: %v", err)
				mu.Lock()
				result.Hosts[hostname] = hd
				mu.Unlock()
				return
			}

			resp, err := m.client.Do(req)
			if err != nil {
				hd.Error = fmt.Sprintf("fetching data: %v", err)
				mu.Lock()
				result.Hosts[hostname] = hd
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				hd.Error = fmt.Sprintf("cgtop returned status %d", resp.StatusCode)
				mu.Lock()
				result.Hosts[hostname] = hd
				mu.Unlock()
				return
			}

			var apiResp APIResponse
			if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
				hd.Error = fmt.Sprintf("decoding response: %v", err)
				mu.Lock()
				result.Hosts[hostname] = hd
				mu.Unlock()
				return
			}

			hd.Data = &apiResp
			mu.Lock()
			result.Hosts[hostname] = hd
			mu.Unlock()
		}(hostname)
	}

	wg.Wait()
	return result
}
