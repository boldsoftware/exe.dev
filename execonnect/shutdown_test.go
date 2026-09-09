package execonnect

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc/test/bufconn"
)

type blockingEnrollmentServer struct {
	connectapi.UnimplementedExternalConnectionServiceServer
	started  chan struct{}
	canceled chan struct{}
}

func (server *blockingEnrollmentServer) Enroll(ctx context.Context, _ *connectapi.EnrollRequest) (*connectapi.EnrollResponse, error) {
	close(server.started)
	<-ctx.Done()
	close(server.canceled)
	return nil, ctx.Err()
}

func TestServeCancelsEnrollmentOnShutdown(t *testing.T) {
	remote := &blockingEnrollmentServer{started: make(chan struct{}), canceled: make(chan struct{})}
	client, closeClient := newControlTestClient(t, remote)
	defer closeClient()
	serveContext, cancelServe := context.WithCancel(t.Context())
	enrollContext, cancelEnroll := context.WithCancel(t.Context())
	stateDir := t.TempDir()
	started := make(chan struct{})
	stopped := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = serve(serveContext, ServeOptions{
			StateDir: stateDir,
			OnEvent: func(event ServeEvent) {
				if event.Kind == ServeStarted {
					close(started)
				}
			},
		}, func(string) (connectorClient, error) {
			return &ExedClient{ExternalConnectionServiceClient: client}, nil
		})
		close(stopped)
	}()
	t.Cleanup(func() {
		cancelEnroll()
		cancelServe()
		<-stopped
	})
	select {
	case <-started:
	case <-stopped:
		t.Fatalf("serve stopped before startup: %v", serveErr)
	}
	enrolled := make(chan error, 1)
	go func() {
		_, err := Enroll(enrollContext, EnrollOptions{
			StateDir:  stateDir,
			ReadToken: func() (string, error) { return "token", nil },
		})
		enrolled <- err
	}()
	<-remote.started
	cancelServe()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon shutdown did not cancel the enrollment RPC")
	}
	if serveErr != nil {
		t.Fatalf("serve: %v", serveErr)
	}
	<-remote.canceled
	if err := <-enrolled; err == nil {
		t.Fatal("canceled enrollment succeeded")
	}
	state, err := LoadState(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state.Enrolled {
		t.Fatal("canceled enrollment persisted an identity")
	}
}

func TestControlServerBoundsShutdownAndClosesActiveRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		listener := bufconn.Listen(64 << 10)
		defer listener.Close()
		started := make(chan struct{})
		release := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- runControlServer(ctx, listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				close(started)
				<-release
			}))
		}()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}}
		defer transport.CloseIdleConnections()
		requestDone := make(chan error, 1)
		go func() {
			response, err := (&http.Client{Transport: transport}).Get("http://control/status")
			if response != nil {
				response.Body.Close()
			}
			requestDone <- err
		}()
		<-started
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("shutdown error = %v, want deadline exceeded", err)
			}
			if err := <-requestDone; err == nil {
				t.Error("active request was not closed on shutdown timeout")
			}
			close(release)
		case <-time.After(10 * time.Second):
			t.Error("control server shutdown has no deadline")
			close(release)
			<-result
			<-requestDone
		}
	})
}

func TestControlServerCancelsRequestsOnReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := bufconn.Listen(64 << 10)
		defer listener.Close()
		started := make(chan context.Context, 1)
		result := make(chan error, 1)
		go func() {
			result <- runControlServer(t.Context(), listener, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				started <- request.Context()
				<-request.Context().Done()
			}))
		}()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}}
		defer transport.CloseIdleConnections()
		clientContext, cancelClient := context.WithCancel(t.Context())
		defer cancelClient()
		request, err := http.NewRequestWithContext(clientContext, http.MethodGet, "http://control/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		clientDone := make(chan struct{})
		go func() {
			response, _ := (&http.Client{Transport: transport}).Do(request)
			if response != nil {
				response.Body.Close()
			}
			close(clientDone)
		}()
		requestContext := <-started
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err == nil {
			t.Error("closed listener did not stop the control server with an error")
		}
		if err := requestContext.Err(); !errors.Is(err, context.Canceled) {
			t.Errorf("request context after server return = %v, want canceled", err)
		}
		if err := t.Context().Err(); err != nil {
			t.Errorf("caller context was canceled: %v", err)
		}
		cancelClient()
		<-clientDone
	})
}
