package execonnect

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type reconnectingControlServer struct {
	connectapi.UnimplementedExternalConnectionServiceServer

	mu            sync.Mutex
	authorization []string
	hellos        []*connectapi.ConnectorHello
	snapshots     []*connectapi.EndpointSnapshot
	connected     chan struct{}
}

func (server *reconnectingControlServer) Connect(stream grpc.BidiStreamingServer[connectapi.ConnectorMessage, connectapi.ControlMessage]) error {
	incoming, _ := metadata.FromIncomingContext(stream.Context())
	helloMessage, err := stream.Recv()
	if err != nil {
		return err
	}
	snapshotMessage, err := stream.Recv()
	if err != nil {
		return err
	}

	server.mu.Lock()
	server.authorization = append(server.authorization, incoming.Get("authorization")...)
	server.hellos = append(server.hellos, helloMessage.GetHello())
	server.snapshots = append(server.snapshots, snapshotMessage.GetEndpointSnapshot())
	connection := len(server.hellos)
	server.mu.Unlock()
	if connection == 1 {
		return status.Error(codes.Unavailable, "reconnect")
	}
	close(server.connected)
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestRunControlStreamAuthenticatesPublishesAndReplaysAfterReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &reconnectingControlServer{connected: make(chan struct{})}
		client, closeClient := newControlTestClient(t, service)
		defer closeClient()
		endpoint, err := NormalizeEndpoint("database", "tcp://database.internal:5432", "")
		if err != nil {
			t.Fatal(err)
		}
		updates := make(chan EndpointUpdate, 1)
		updates <- EndpointUpdate{Endpoints: []Endpoint{endpoint}}
		events := make(chan ControlEvent, 16)
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			result <- RunControlStream(ctx, client, Credentials{
				ExternalConnectionID: "ec_test",
				ConnectorSecret:      "ecs_test",
			}, "nodekey:connector", updates, nil, func(event ControlEvent) error {
				events <- event
				return nil
			})
		}()

		<-service.connected
		cancel()
		if err := <-result; err != nil {
			t.Fatal(err)
		}

		service.mu.Lock()
		defer service.mu.Unlock()
		if len(service.authorization) != 2 || service.authorization[0] != "Bearer ecs_test" || service.authorization[1] != "Bearer ecs_test" {
			t.Fatalf("authorization = %#v", service.authorization)
		}
		if len(service.hellos) != 2 {
			t.Fatalf("hellos = %#v", service.hellos)
		}
		if connectapi.LegacyCapabilityVersion != 0 {
			t.Fatalf("legacy capability = %d, want 0", connectapi.LegacyCapabilityVersion)
		}
		if connectapi.InitialCapabilityVersion != 1 {
			t.Fatalf("initial capability = %d, want 1", connectapi.InitialCapabilityVersion)
		}
		if connectapi.WireGuardRehandshakeCapabilityVersion != 2 {
			t.Fatalf("WireGuard re-handshake capability = %d, want 2", connectapi.WireGuardRehandshakeCapabilityVersion)
		}
		if connectapi.CurrentCapabilityVersion != connectapi.WireGuardRehandshakeCapabilityVersion {
			t.Fatalf("current capability = %d, want WireGuard re-handshake capability %d", connectapi.CurrentCapabilityVersion, connectapi.WireGuardRehandshakeCapabilityVersion)
		}
		for _, hello := range service.hellos {
			if hello.GetExternalConnectionID() != "ec_test" || hello.GetWireguardPublicKey() != "nodekey:connector" || hello.GetCapabilityVersion() != connectapi.CurrentCapabilityVersion || hello.GetBuildVersion() != BuildRevision() {
				t.Fatalf("hello = %#v", hello)
			}
		}
		if len(service.snapshots) != 2 {
			t.Fatalf("snapshots = %#v", service.snapshots)
		}
		for _, snapshot := range service.snapshots {
			if len(snapshot.GetEndpoints()) != 1 || snapshot.GetEndpoints()[0].GetKey() != "database" || snapshot.GetEndpoints()[0].GetTargetPort() != 5432 {
				t.Fatalf("snapshot = %#v", snapshot)
			}
		}

		var connected, retrying, sent int
		for len(events) > 0 {
			event := <-events
			switch event.Kind {
			case ControlConnected:
				connected++
			case ControlRetrying:
				retrying++
				if event.Attempt != 1 || event.Delay != controlStreamRetryDelay(0) {
					t.Fatalf("retry event = %#v", event)
				}
			case ControlSnapshotSent:
				sent++
			}
		}
		if connected != 2 || retrying != 1 || sent != 2 {
			t.Fatalf("events: connected=%d retrying=%d sent=%d", connected, retrying, sent)
		}
	})
}

type rejectingControlServer struct {
	connectapi.UnimplementedExternalConnectionServiceServer
	mu          sync.Mutex
	connections int
}

func (server *rejectingControlServer) Connect(grpc.BidiStreamingServer[connectapi.ConnectorMessage, connectapi.ControlMessage]) error {
	server.mu.Lock()
	server.connections++
	server.mu.Unlock()
	return status.Error(codes.Unauthenticated, "bad credentials")
}

func TestRunControlStreamStopsOnTerminalGRPCError(t *testing.T) {
	service := &rejectingControlServer{}
	client, closeClient := newControlTestClient(t, service)
	defer closeClient()
	updates := make(chan EndpointUpdate, 1)
	updates <- EndpointUpdate{}
	events := make(chan ControlEvent, 8)
	err := RunControlStream(t.Context(), client, Credentials{
		ExternalConnectionID: "ec_test",
		ConnectorSecret:      "ecs_test",
	}, "nodekey:connector", updates, nil, func(event ControlEvent) error {
		events <- event
		return nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("RunControlStream error = %v", err)
	}
	service.mu.Lock()
	connections := service.connections
	service.mu.Unlock()
	if connections != 1 {
		t.Fatalf("connections = %d, want 1", connections)
	}
	var terminal bool
	for len(events) > 0 {
		event := <-events
		if event.Kind == ControlRetrying {
			t.Fatalf("terminal failure retried: %#v", event)
		}
		if event.Kind == ControlDisconnected && event.Terminal {
			terminal = true
		}
	}
	if !terminal {
		t.Fatal("missing terminal disconnected event")
	}
}

func TestControlErrorClassification(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		err       error
		transient bool
	}{
		{name: "unavailable", err: status.Error(codes.Unavailable, "down"), transient: true},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "slow"), transient: true},
		{name: "internal", err: status.Error(codes.Internal, "transport"), transient: true},
		{name: "eof", err: io.EOF, transient: true},
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "bad")},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "bad")},
		{name: "invalid argument", err: status.Error(codes.InvalidArgument, "bad")},
		{name: "failed precondition", err: status.Error(codes.FailedPrecondition, "bad")},
		{name: "local configuration", err: errors.New("invalid snapshot")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isTransientControlError(testCase.err); got != testCase.transient {
				t.Fatalf("isTransientControlError() = %v, want %v", got, testCase.transient)
			}
		})
	}
}

func TestRunControlStreamValidatesConfiguration(t *testing.T) {
	validCredentials := Credentials{ExternalConnectionID: "ec_test", ConnectorSecret: "ecs_test"}
	for name, testCase := range map[string]struct {
		client      connectapi.ExternalConnectionServiceClient
		credentials Credentials
		publicKey   string
		updates     <-chan EndpointUpdate
	}{
		"nil client":       {credentials: validCredentials, publicKey: "nodekey:test", updates: make(chan EndpointUpdate)},
		"bad credentials":  {client: &controlClientStub{}, publicKey: "nodekey:test", updates: make(chan EndpointUpdate)},
		"empty public key": {client: &controlClientStub{}, credentials: validCredentials, updates: make(chan EndpointUpdate)},
		"nil updates":      {client: &controlClientStub{}, credentials: validCredentials, publicKey: "nodekey:test"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := RunControlStream(t.Context(), testCase.client, testCase.credentials, testCase.publicKey, testCase.updates, nil, nil); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

type controlClientStub struct {
	connectapi.ExternalConnectionServiceClient
}

func newControlTestClient(t *testing.T, service connectapi.ExternalConnectionServiceServer) (connectapi.ExternalConnectionServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	connectapi.RegisterExternalConnectionServiceServer(server, service)
	go func() {
		if err := server.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			panic(err)
		}
	}()
	connection, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return connectapi.NewExternalConnectionServiceClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}
