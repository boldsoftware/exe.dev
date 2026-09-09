package execonnect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"testing"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc"
)

type enrollmentServerStub struct {
	connectapi.UnimplementedExternalConnectionServiceServer
	request *connectapi.EnrollRequest
}

func (server *enrollmentServerStub) Enroll(_ context.Context, request *connectapi.EnrollRequest) (*connectapi.EnrollResponse, error) {
	server.request = request
	return &connectapi.EnrollResponse{
		ExternalConnectionID: "ec_tls",
		ConnectorSecret:      "ecs_tls",
	}, nil
}

func TestDialExedUsesHTTPSGRPC(t *testing.T) {
	service := &enrollmentServerStub{}
	grpcServer := grpc.NewServer()
	connectapi.RegisterExternalConnectionServiceServer(grpcServer, service)
	server := httptest.NewUnstartedServer(grpcServer)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(func() {
		server.Close()
		grpcServer.Stop()
	})

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := dialExed(server.URL, roots)
	if err != nil {
		t.Fatalf("dialExed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	response, err := client.Enroll(t.Context(), &connectapi.EnrollRequest{EnrollmentToken: "token"})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if response.GetExternalConnectionID() != "ec_tls" || service.request.GetEnrollmentToken() != "token" {
		t.Fatalf("response = %+v, request = %+v", response, service.request)
	}
}

func TestDialExedRejectsInvalidURLs(t *testing.T) {
	for _, rawURL := range []string{
		"",
		"http://example.com",
		"https://",
		"https://user@example.com",
		"https://example.com/path",
		"https://example.com?query=yes",
		"https://example.com/#fragment",
		" https://example.com",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := dialExed(rawURL, x509.NewCertPool()); err == nil {
				t.Fatalf("dialExed(%q) succeeded", rawURL)
			}
		})
	}
}

func TestDialExedRequiresTLS12(t *testing.T) {
	config := exedTLSConfig("example.com", nil)
	if config.MinVersion != tls.VersionTLS12 || config.ServerName != "example.com" {
		t.Fatalf("TLS config = %+v", config)
	}
}
