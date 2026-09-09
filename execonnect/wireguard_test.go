package execonnect

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel/wgnet"

	"google.golang.org/protobuf/proto"
	"tailscale.com/types/key"
)

func TestWireGuardTunnelAppliesAssignment(t *testing.T) {
	connectorKey := key.NewNode()
	connectorAddress := netip.MustParseAddr("fd65:7865::102:304")

	tunnel, err := NewWireGuardTunnel(connectorKey)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()

	serverKey := key.NewNode()
	server := newWireGuardTestServer(t, serverKey, connectorKey.Public(), connectorAddress)
	assignment := wireGuardTestAssignment(t, serverKey.Public(), server, connectorAddress)
	if err := tunnel.HandleControlMessage(t.Context(), &connectapi.ControlMessage{Payload: &connectapi.ControlMessage_WireguardAssignment{WireguardAssignment: assignment}}); err != nil {
		t.Fatal(err)
	}
	network := tunnel.Net()
	if err := tunnel.HandleControlMessage(t.Context(), &connectapi.ControlMessage{Payload: &connectapi.ControlMessage_WireguardRehandshake{WireguardRehandshake: &connectapi.WireGuardRehandshake{}}}); err != nil {
		t.Fatalf("re-handshake event: %v", err)
	}
	if tunnel.Net() != network {
		t.Fatal("re-handshake rebuilt the userspace network")
	}
	if err := tunnel.ApplyAssignment(assignment); err != nil {
		t.Fatalf("repeat assignment: %v", err)
	}
	if tunnel.Net() != network {
		t.Fatal("restored assignment rebuilt the userspace network")
	}
	assertWireGuardTunnelTCP(t, tunnel, server, "through initial WireGuard peer")

	replacementKey := key.NewNode()
	replacement := newWireGuardTestServer(t, replacementKey, connectorKey.Public(), connectorAddress)
	if err := tunnel.ApplyAssignment(wireGuardTestAssignment(t, replacementKey.Public(), replacement, connectorAddress)); err != nil {
		t.Fatalf("replace assignment: %v", err)
	}
	assertWireGuardTunnelTCP(t, tunnel, replacement, "through replacement WireGuard peer")
}

func assertWireGuardTunnelTCP(t *testing.T, tunnel *WireGuardTunnel, server *wgnet.Device, payload string) {
	t.Helper()
	listener, err := server.Net().ListenTCP(4700)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		_, err = io.Copy(connection, connection)
		serverResult <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	connection, err := tunnel.Net().DialContextTCP(ctx, netip.AddrPortFrom(vpctunnel.ServerTunnelAddress(), 4700))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(connection, payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatalf("response = %q, want %q", response, payload)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWireGuardTunnelRejectsInvalidAssignments(t *testing.T) {
	tunnel, err := NewWireGuardTunnel(key.NewNode())
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	valid := &connectapi.WireGuardAssignment{
		ConnectorTunnelAddress: "fd65:7865::102:304",
		ServerTunnelAddress:    vpctunnel.ServerTunnelAddress().String(),
		PeerPublicKey:          key.NewNode().Public().String(),
		PeerEndpoint:           "127.0.0.1:51820",
		Mtu:                    uint32(vpctunnel.TunnelMTU()),
	}
	tests := []struct {
		name   string
		mutate func(*connectapi.WireGuardAssignment)
	}{
		{name: "missing connector address", mutate: func(a *connectapi.WireGuardAssignment) { a.ConnectorTunnelAddress = "" }},
		{name: "connector outside prefix", mutate: func(a *connectapi.WireGuardAssignment) { a.ConnectorTunnelAddress = "fd00::1" }},
		{name: "connector is server", mutate: func(a *connectapi.WireGuardAssignment) {
			a.ConnectorTunnelAddress = vpctunnel.ServerTunnelAddress().String()
		}},
		{name: "wrong server address", mutate: func(a *connectapi.WireGuardAssignment) { a.ServerTunnelAddress = "fd65:7865::2" }},
		{name: "invalid public key", mutate: func(a *connectapi.WireGuardAssignment) { a.PeerPublicKey = "invalid" }},
		{name: "missing endpoint", mutate: func(a *connectapi.WireGuardAssignment) { a.PeerEndpoint = "" }},
		{name: "wrong MTU", mutate: func(a *connectapi.WireGuardAssignment) { a.Mtu++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment := proto.Clone(valid).(*connectapi.WireGuardAssignment)
			test.mutate(assignment)
			if err := tunnel.ApplyAssignment(assignment); err == nil {
				t.Fatal("assignment succeeded")
			}
		})
	}
	if err := tunnel.HandleControlMessage(t.Context(), &connectapi.ControlMessage{}); err == nil {
		t.Fatal("unknown control message succeeded")
	}
}

func newWireGuardTestServer(t *testing.T, privateKey key.NodePrivate, connectorPublicKey key.NodePublic, connectorAddress netip.Addr) *wgnet.Device {
	t.Helper()
	server, err := wgnet.New(wgnet.Config{
		PrivateKey:     privateKey,
		LocalAddresses: []netip.Addr{vpctunnel.ServerTunnelAddress()},
		MTU:            vpctunnel.TunnelMTU(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if err := server.UpsertPeer(wgnet.Peer{
		PublicKey:  connectorPublicKey,
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(connectorAddress, 128)},
	}); err != nil {
		t.Fatal(err)
	}
	return server
}

func wireGuardTestAssignment(t *testing.T, serverPublicKey key.NodePublic, server *wgnet.Device, connectorAddress netip.Addr) *connectapi.WireGuardAssignment {
	t.Helper()
	port, err := server.ListenPort()
	if err != nil {
		t.Fatal(err)
	}
	return &connectapi.WireGuardAssignment{
		ConnectorTunnelAddress: connectorAddress.String(),
		ServerTunnelAddress:    vpctunnel.ServerTunnelAddress().String(),
		PeerPublicKey:          serverPublicKey.String(),
		PeerEndpoint:           net.JoinHostPort("127.0.0.1", fmt.Sprint(port)),
		Mtu:                    uint32(vpctunnel.TunnelMTU()),
	}
}
