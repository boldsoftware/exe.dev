package execonnect

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel/nstun"
)

// FlowServer accepts tunneled flows and proxies them to registered endpoints.
type FlowServer struct {
	ctx                          context.Context
	expectedExternalConnectionID string
	dial                         EndpointDialer
	onError                      func(error)

	mu       sync.Mutex
	network  *nstun.Net
	listener net.Listener
	closed   bool
}

// NewFlowServer creates a flow server that starts listening when SetNetwork is called.
func NewFlowServer(ctx context.Context, expectedExternalConnectionID string, dial EndpointDialer, onError func(error)) (*FlowServer, error) {
	if ctx == nil {
		return nil, errors.New("nil flow server context")
	}
	expectedExternalConnectionID = strings.TrimSpace(expectedExternalConnectionID)
	if expectedExternalConnectionID == "" {
		return nil, errors.New("empty expected External Connection ID")
	}
	if dial == nil {
		return nil, errors.New("nil endpoint dialer")
	}
	server := &FlowServer{
		ctx:                          ctx,
		expectedExternalConnectionID: expectedExternalConnectionID,
		dial:                         dial,
		onError:                      onError,
	}
	context.AfterFunc(ctx, server.Close)
	return server, nil
}

// SetNetwork moves the flow listener to network. Reapplying the same network is a no-op.
func (server *FlowServer) SetNetwork(network *nstun.Net) error {
	if network == nil {
		return errors.New("nil flow network")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return net.ErrClosed
	}
	if server.network == network {
		return nil
	}
	listener, err := network.ListenTCP(vpctunnel.FlowPort())
	if err != nil {
		return err
	}
	oldListener := server.listener
	server.network = network
	server.listener = listener
	if oldListener != nil {
		oldListener.Close()
	}
	go server.serve(listener)
	return nil
}

func (server *FlowServer) serve(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go server.handleConnection(connection)
	}
}

func (server *FlowServer) handleConnection(connection net.Conn) {
	if err := HandleFlow(server.ctx, connection, server.expectedExternalConnectionID, server.dial); err != nil &&
		!errors.Is(err, errEndpointFlowTerminated) && server.ctx.Err() == nil && server.onError != nil {
		server.onError(err)
	}
}

// Close stops accepting new flows. Active flows close through the server context.
func (server *FlowServer) Close() {
	if server == nil {
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return
	}
	server.closed = true
	if server.listener != nil {
		server.listener.Close()
		server.listener = nil
	}
}
