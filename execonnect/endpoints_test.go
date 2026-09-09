package execonnect

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestEndpointRegistryReplacePreservesFlowForUnchangedRoute(t *testing.T) {
	address, accepted, received, result := probeTCPServer(t)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := Endpoint{Key: "database", Target: address}
	registry, err := NewEndpointRegistry([]Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := registry.DialContext(t.Context(), FlowContext{}, "database", mustPort(t, port))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	<-accepted

	if err := registry.Replace([]Endpoint{endpoint}); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte{1}); err != nil {
		t.Fatalf("unchanged route closed flow: %v", err)
	}
	if got := <-received; got != 1 {
		t.Fatalf("server received %d, want 1", got)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestEndpointRegistryReplaceTerminatesFlowsForRoutingChanges(t *testing.T) {
	tests := []struct {
		name   string
		before func(string) Endpoint
		after  func(string) Endpoint
	}{
		{
			name: "target",
			before: func(address string) Endpoint {
				return Endpoint{Key: "api", Target: address}
			},
			after: func(string) Endpoint {
				return Endpoint{Key: "api", Target: "127.0.0.1:1"}
			},
		},
		{
			name: "protocol",
			before: func(address string) Endpoint {
				return Endpoint{Key: "api", URL: "tcp://" + address}
			},
			after: func(address string) Endpoint {
				return Endpoint{Key: "api", URL: "http://" + address}
			},
		},
		{
			name: "TLS server name",
			before: func(address string) Endpoint {
				return Endpoint{Key: "api", URL: "https://" + address, TLSServerName: "old.internal"}
			},
			after: func(address string) Endpoint {
				return Endpoint{Key: "api", URL: "https://" + address, TLSServerName: "new.internal"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, accepted, _, result := probeTCPServer(t)
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewEndpointRegistry([]Endpoint{test.before(address)})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := registry.DialContext(t.Context(), FlowContext{}, "api", mustPort(t, port))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			<-accepted

			if err := registry.Replace([]Endpoint{test.after(address)}); err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Write([]byte{1}); err == nil {
				t.Fatal("routing update left the old flow open")
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEndpointRegistryReplaceWaitsForAttachedConnectionClose(t *testing.T) {
	registry, err := NewEndpointRegistry([]Endpoint{{Key: "database", Target: "127.0.0.1:5432"}})
	if err != nil {
		t.Fatal(err)
	}
	local, peer := net.Pipe()
	defer peer.Close()
	closeStarted := make(chan struct{})
	allowClose := make(chan struct{})
	registry.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return &blockingCloseConnection{Conn: local, started: closeStarted, allow: allowClose}, nil
	}
	connection, err := registry.DialContext(t.Context(), FlowContext{}, "database", 5432)
	if err != nil {
		t.Fatal(err)
	}

	replaced := make(chan error, 1)
	go func() {
		replaced <- registry.Replace(nil)
	}()
	<-closeStarted
	select {
	case err := <-replaced:
		t.Fatalf("Replace returned before connection Close completed: %v", err)
	default:
	}
	close(allowClose)
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte{1}); err == nil {
		t.Fatal("terminated connection remained writable")
	}
}

func TestEndpointRegistryReplaceRejectsDialThatAttachesAfterRemoval(t *testing.T) {
	registry, err := NewEndpointRegistry([]Endpoint{{Key: "database", Target: "127.0.0.1:5432"}})
	if err != nil {
		t.Fatal(err)
	}
	local, peer := net.Pipe()
	defer peer.Close()
	dialStarted := make(chan struct{})
	registry.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return local, nil
	}

	type dialResult struct {
		connection net.Conn
		err        error
	}
	dialResultChannel := make(chan dialResult, 1)
	go func() {
		connection, err := registry.DialContext(t.Context(), FlowContext{}, "database", 5432)
		dialResultChannel <- dialResult{connection: connection, err: err}
	}()
	<-dialStarted

	if err := registry.Replace(nil); err != nil {
		t.Fatal(err)
	}
	result := <-dialResultChannel
	if result.connection != nil {
		result.connection.Close()
		t.Fatal("dial returned a connection after endpoint removal")
	}
	if !errors.Is(result.err, errEndpointFlowTerminated) {
		t.Fatalf("dial error = %v, want endpoint flow termination", result.err)
	}
	var buffer [1]byte
	if _, err := peer.Read(buffer[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("late dial peer read error = %v, want EOF", err)
	}
}

func TestEndpointRegistryRejectsUnknownEndpointAndPortMismatch(t *testing.T) {
	registry, err := NewEndpointRegistry([]Endpoint{{Key: "database", Target: "127.0.0.1:5432"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.DialContext(t.Context(), FlowContext{}, "missing", 5432); err == nil {
		t.Fatal("expected unknown endpoint error")
	}
	if _, err := registry.DialContext(t.Context(), FlowContext{}, "database", 5433); err == nil {
		t.Fatal("expected target port mismatch error")
	}
}

func TestEndpointRegistryRejectsInvalidSnapshots(t *testing.T) {
	tests := []struct {
		name      string
		endpoints []Endpoint
	}{
		{name: "empty key", endpoints: []Endpoint{{Target: "127.0.0.1:5432"}}},
		{name: "duplicate key", endpoints: []Endpoint{{Key: "db", Target: "127.0.0.1:5432"}, {Key: "db", Target: "127.0.0.1:5433"}}},
		{name: "missing port", endpoints: []Endpoint{{Key: "db", Target: "localhost"}}},
		{name: "zero port", endpoints: []Endpoint{{Key: "db", Target: "localhost:0"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewEndpointRegistry(test.endpoints); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type blockingCloseConnection struct {
	net.Conn
	started chan<- struct{}
	allow   <-chan struct{}
}

func (connection *blockingCloseConnection) Close() error {
	close(connection.started)
	<-connection.allow
	return connection.Conn.Close()
}

func probeTCPServer(t *testing.T) (string, <-chan struct{}, <-chan byte, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{})
	received := make(chan byte, 1)
	result := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer connection.Close()
		close(accepted)
		var buffer [1]byte
		for {
			_, err := io.ReadFull(connection, buffer[:])
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				result <- err
				return
			}
			received <- buffer[0]
		}
	}()
	return listener.Addr().String(), accepted, received, result
}

func mustPort(t *testing.T, port string) int {
	t.Helper()
	parsed, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

var _ EndpointDialer = (*EndpointRegistry)(nil).DialContext
