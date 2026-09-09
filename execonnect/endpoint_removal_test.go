package execonnect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"
)

func TestEndpointRemoveTerminatesOpenAndNewFlowsUntilReadded(t *testing.T) {
	var upstreamRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	endpoint, target, err := normalizeEndpoint(Endpoint{Key: "api", URL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	updates := make(chan EndpointUpdate, 1)
	registry := startLocalControl(t, stateDir, EndpointRegistrySnapshot{Endpoints: []Endpoint{endpoint}}, updates)

	flowListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer flowListener.Close()
	flowResults := make(chan error, 4)
	go func() {
		for {
			connection, err := flowListener.Accept()
			if err != nil {
				return
			}
			go func() {
				flowResults <- HandleFlow(t.Context(), connection, "ec_test", registry.DialContext)
			}()
		}
	}()

	var flowCount atomic.Int64
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			connection, err := dialer.DialContext(ctx, "tcp", flowListener.Addr().String())
			if err != nil {
				return nil, err
			}
			header, err := vpctunnel.FormatFlowProxyV2Header("ec_test", "api", target.port, execonnectTestProxyFlowContext)
			if err != nil {
				connection.Close()
				return nil, err
			}
			if _, err := connection.Write(header); err != nil {
				connection.Close()
				return nil, err
			}
			flowCount.Add(1)
			return connection, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request := func() error {
		response, err := client.Get("http://api.internal/")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if string(body) != "ok" {
			t.Fatalf("body = %q, want ok", body)
		}
		return nil
	}

	if err := request(); err != nil {
		t.Fatalf("initial request: %v", err)
	}
	if got := upstreamRequests.Load(); got != 1 {
		t.Fatalf("initial upstream requests = %d, want 1", got)
	}
	if got := flowCount.Load(); got != 1 {
		t.Fatalf("initial flow count = %d, want 1", got)
	}

	removed, err := RemoveEndpoint(t.Context(), EndpointNameOptions{StateDir: stateDir, Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Endpoints) != 0 {
		t.Fatalf("removed snapshot = %+v, want no endpoints", removed)
	}
	if update := <-updates; len(update.Endpoints) != 0 {
		t.Fatalf("removed publication = %+v, want no endpoints", update)
	}

	if err := request(); err == nil {
		t.Fatal("request after endpoint remove reached the withdrawn endpoint")
	}
	if got := upstreamRequests.Load(); got != 1 {
		t.Fatalf("post-remove upstream requests = %d, want 1", got)
	}
	if got := flowCount.Load(); got != 2 {
		t.Fatalf("post-remove flow count = %d, want 2", got)
	}

	var closedOpenFlow bool
	var rejectedNewFlow bool
	for range 2 {
		err := <-flowResults
		switch {
		case err == nil:
			closedOpenFlow = true
		case strings.Contains(err.Error(), `unknown endpoint "api"`):
			rejectedNewFlow = true
		default:
			t.Fatalf("flow result = %v", err)
		}
	}
	if !closedOpenFlow || !rejectedNewFlow {
		t.Fatalf("closedOpenFlow=%v rejectedNewFlow=%v", closedOpenFlow, rejectedNewFlow)
	}

	readded, changed, err := AddEndpoint(t.Context(), EndpointOptions{StateDir: stateDir, Name: "api", URL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(readded.Endpoints) != 1 {
		t.Fatalf("readded snapshot = %+v, changed=%v", readded, changed)
	}
	if update := <-updates; len(update.Endpoints) != 1 || update.Endpoints[0].Key != "api" {
		t.Fatalf("readded publication = %+v", update)
	}
	if err := request(); err != nil {
		t.Fatalf("request after re-add: %v", err)
	}
	if got := upstreamRequests.Load(); got != 2 {
		t.Fatalf("readded upstream requests = %d, want 2", got)
	}
	if got := flowCount.Load(); got != 3 {
		t.Fatalf("readded flow count = %d, want 3", got)
	}
}
