// Package execonnect implements the External Connection connector data plane.
package execonnect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"

	"github.com/pires/go-proxyproto"
)

// FlowContext identifies the integration and originating VM for one flow.
type FlowContext struct {
	IntegrationName string
	VMName          string
}

// EndpointDialer opens a local connection for a published endpoint and target port.
type EndpointDialer func(ctx context.Context, flowContext FlowContext, endpoint string, targetPort int) (net.Conn, error)

// HandleFlow proxies one accepted External Connection flow to its local endpoint.
// HandleFlow owns and closes the accepted connection.
func HandleFlow(ctx context.Context, connection net.Conn, expectedExternalConnectionID string, dial EndpointDialer) error {
	if connection == nil {
		return errors.New("nil flow connection")
	}
	defer connection.Close()
	stopConnectionClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopConnectionClose()

	expectedExternalConnectionID = strings.TrimSpace(expectedExternalConnectionID)
	if expectedExternalConnectionID == "" {
		return errors.New("empty expected External Connection ID")
	}
	if dial == nil {
		return errors.New("nil endpoint dialer")
	}

	reader := bufio.NewReader(connection)
	header, err := proxyproto.Read(reader)
	if err != nil {
		return fmt.Errorf("read PROXY v2 flow header: %w", err)
	}
	externalConnectionID, endpoint, targetPort, parsedFlowContext, err := vpctunnel.FlowMetadataFromProxyV2Header(header)
	if err != nil {
		return fmt.Errorf("parse PROXY v2 flow header: %w", err)
	}
	if externalConnectionID != expectedExternalConnectionID {
		return fmt.Errorf("unexpected External Connection ID %q", externalConnectionID)
	}

	flowContext := FlowContext{
		IntegrationName: parsedFlowContext.IntegrationName,
		VMName:          parsedFlowContext.VMName,
	}
	localConnection, err := dial(ctx, flowContext, endpoint, targetPort)
	if err != nil {
		return fmt.Errorf("dial endpoint %q: %w", endpoint, err)
	}
	if localConnection == nil {
		return fmt.Errorf("dial endpoint %q: nil connection", endpoint)
	}
	defer localConnection.Close()
	stopLocalClose := context.AfterFunc(ctx, func() { _ = localConnection.Close() })
	defer stopLocalClose()

	vpctunnel.Splice(&bufferedConn{Conn: connection, reader: reader}, localConnection)
	return nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

func (c *bufferedConn) CloseWrite() error {
	if connection, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return connection.CloseWrite()
	}
	return c.Conn.Close()
}
