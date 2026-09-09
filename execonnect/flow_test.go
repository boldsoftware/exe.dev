package execonnect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"
	"github.com/pires/go-proxyproto"
)

var (
	execonnectTestFlowContext      = FlowContext{IntegrationName: "office-api", VMName: "dev-box"}
	execonnectTestProxyFlowContext = vpctunnel.FlowContext{IntegrationName: "office-api", VMName: "dev-box"}
)

func TestHandleFlowRejectsMissingContextBeforeDialing(t *testing.T) {
	tests := []struct {
		name string
		tlvs []proxyproto.TLV
	}{
		{
			name: "missing all context",
			tlvs: []proxyproto.TLV{
				{Type: vpctunnel.ExternalConnectionIDTLVType, Value: []byte("external-123")},
				{Type: vpctunnel.EndpointTLVType, Value: []byte("postgres")},
			},
		},
		{
			name: "missing integration name",
			tlvs: []proxyproto.TLV{
				{Type: vpctunnel.ExternalConnectionIDTLVType, Value: []byte("external-123")},
				{Type: vpctunnel.EndpointTLVType, Value: []byte("postgres")},
				{Type: vpctunnel.VMNameTLVType, Value: []byte("dev-box")},
			},
		},
		{
			name: "missing VM name",
			tlvs: []proxyproto.TLV{
				{Type: vpctunnel.ExternalConnectionIDTLVType, Value: []byte("external-123")},
				{Type: vpctunnel.EndpointTLVType, Value: []byte("postgres")},
				{Type: vpctunnel.IntegrationNameTLVType, Value: []byte("office-api")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			var dialed atomic.Bool
			result := make(chan error, 1)
			go func() {
				result <- HandleFlow(t.Context(), server, "external-123", func(context.Context, FlowContext, string, int) (net.Conn, error) {
					dialed.Store(true)
					return nil, nil
				})
			}()

			header := proxyproto.HeaderProxyFromAddrs(
				2,
				&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)},
				&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5432},
			)
			if err := header.SetTLVs(test.tlvs); err != nil {
				t.Fatal(err)
			}
			raw, err := header.Format()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil || !strings.Contains(err.Error(), "name TLV") {
				t.Fatalf("error = %v, want missing context error", err)
			}
			if dialed.Load() {
				t.Fatal("endpoint dialer was called")
			}
		})
	}
}

func TestHandleFlowDialsEndpointAndPreservesPayload(t *testing.T) {
	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localListener.Close()
	localResult := make(chan error, 1)
	go func() {
		connection, err := localListener.Accept()
		if err != nil {
			localResult <- err
			return
		}
		defer connection.Close()
		_, err = io.Copy(connection, connection)
		localResult <- err
	}()

	flowListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer flowListener.Close()
	handlerResult := make(chan error, 1)
	go func() {
		connection, err := flowListener.Accept()
		if err != nil {
			handlerResult <- err
			return
		}
		handlerResult <- HandleFlow(t.Context(), connection, "external-123", func(ctx context.Context, flowContext FlowContext, endpoint string, targetPort int) (net.Conn, error) {
			if flowContext != execonnectTestFlowContext {
				return nil, fmt.Errorf("flow context = %+v, want %+v", flowContext, execonnectTestFlowContext)
			}
			if endpoint != "postgres" {
				return nil, fmt.Errorf("endpoint = %q, want postgres", endpoint)
			}
			if targetPort != 5432 {
				return nil, fmt.Errorf("target port = %d, want 5432", targetPort)
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", localListener.Addr().String())
		})
	}()

	connection, err := net.Dial("tcp", flowListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	header, err := vpctunnel.FormatFlowProxyV2Header("external-123", "postgres", 5432, execonnectTestProxyFlowContext)
	if err != nil {
		t.Fatal(err)
	}
	const payload = "payload sent with header"
	if _, err := connection.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	if err := connection.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	if err := <-handlerResult; err != nil {
		t.Fatal(err)
	}
	if err := <-localResult; err != nil {
		t.Fatal(err)
	}
}

func TestFlowServerSuppressesOnlyEndpointTerminationErrors(t *testing.T) {
	tests := []struct {
		name      string
		dialError error
		wantError bool
	}{
		{name: "endpoint terminated", dialError: errEndpointFlowTerminated},
		{name: "unrelated failure", dialError: errors.New("dial failed"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverConnection, clientConnection := net.Pipe()
			defer clientConnection.Close()
			reported := make(chan error, 1)
			server, err := NewFlowServer(t.Context(), "external-123", func(context.Context, FlowContext, string, int) (net.Conn, error) {
				return nil, test.dialError
			}, func(err error) {
				reported <- err
			})
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan struct{})
			go func() {
				server.handleConnection(serverConnection)
				close(done)
			}()
			header, err := vpctunnel.FormatFlowProxyV2Header("external-123", "api", 8080, execonnectTestProxyFlowContext)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := clientConnection.Write(header); err != nil {
				t.Fatal(err)
			}
			<-done

			select {
			case err := <-reported:
				if !test.wantError {
					t.Fatalf("reported intentional termination: %v", err)
				}
				if !strings.Contains(err.Error(), "dial failed") {
					t.Fatalf("reported error = %v", err)
				}
			default:
				if test.wantError {
					t.Fatal("unrelated flow error was suppressed")
				}
			}
		})
	}
}

func TestHandleFlowRejectsWrongExternalConnection(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	var dialed atomic.Bool
	result := make(chan error, 1)
	go func() {
		result <- HandleFlow(t.Context(), server, "external-expected", func(context.Context, FlowContext, string, int) (net.Conn, error) {
			dialed.Store(true)
			return nil, nil
		})
	}()

	header, err := vpctunnel.FormatFlowProxyV2Header("external-other", "postgres", 5432, execonnectTestProxyFlowContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	err = <-result
	if err == nil || !strings.Contains(err.Error(), "unexpected External Connection ID") {
		t.Fatalf("error = %v, want External Connection ID mismatch", err)
	}
	if dialed.Load() {
		t.Fatal("endpoint dialer was called")
	}
}
