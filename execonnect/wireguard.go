package execonnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel/nstun"
	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel/wgnet"

	"tailscale.com/types/key"
)

const wireGuardPersistentKeepalive = 25 * time.Second

// WireGuardTunnel owns execonnect's userspace WireGuard device.
type WireGuardTunnel struct {
	mu               sync.RWMutex
	privateKey       key.NodePrivate
	device           *wgnet.Device
	connectorAddress netip.Addr
	mtu              int
	peerPublicKey    key.NodePublic
}

type wireGuardAssignment struct {
	connectorAddress netip.Addr
	serverAddress    netip.Addr
	peerPublicKey    key.NodePublic
	peerEndpoint     string
	mtu              int
}

// NewWireGuardTunnel creates an unassigned tunnel using privateKey.
func NewWireGuardTunnel(privateKey key.NodePrivate) (*WireGuardTunnel, error) {
	if privateKey.IsZero() {
		return nil, errors.New("WireGuard private key must not be zero")
	}
	return &WireGuardTunnel{privateKey: privateKey}, nil
}

// HandleControlMessage applies one WireGuard control event from exed.
func (tunnel *WireGuardTunnel) HandleControlMessage(_ context.Context, message *connectapi.ControlMessage) error {
	if message == nil {
		return errors.New("unknown control message")
	}
	switch message.Payload.(type) {
	case *connectapi.ControlMessage_WireguardAssignment:
		return tunnel.ApplyAssignment(message.GetWireguardAssignment())
	case *connectapi.ControlMessage_WireguardRehandshake:
		return tunnel.forceHandshake()
	default:
		return errors.New("unknown control message")
	}
}

// ApplyAssignment replaces the desired WireGuard tunnel state.
func (tunnel *WireGuardTunnel) ApplyAssignment(message *connectapi.WireGuardAssignment) error {
	assignment, err := parseWireGuardAssignment(message)
	if err != nil {
		return err
	}

	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	peer := wgnet.Peer{
		PublicKey:           assignment.peerPublicKey,
		Endpoint:            assignment.peerEndpoint,
		AllowedIPs:          []netip.Prefix{netip.PrefixFrom(assignment.serverAddress, 128)},
		PersistentKeepalive: wireGuardPersistentKeepalive,
	}
	if tunnel.device != nil && tunnel.connectorAddress == assignment.connectorAddress && tunnel.mtu == assignment.mtu {
		restoredPeer := tunnel.peerPublicKey == assignment.peerPublicKey
		if err := tunnel.device.UpsertPeer(peer); err != nil {
			return fmt.Errorf("update WireGuard peer: %w", err)
		}
		if !tunnel.peerPublicKey.IsZero() && tunnel.peerPublicKey != assignment.peerPublicKey {
			tunnel.device.RemovePeer(tunnel.peerPublicKey)
		}
		tunnel.peerPublicKey = assignment.peerPublicKey
		if restoredPeer {
			if err := tunnel.device.ForceHandshake(tunnel.peerPublicKey); err != nil {
				return fmt.Errorf("force WireGuard handshake after assignment restore: %w", err)
			}
		}
		return nil
	}

	device, err := wgnet.New(wgnet.Config{
		PrivateKey:     tunnel.privateKey,
		LocalAddresses: []netip.Addr{assignment.connectorAddress},
		MTU:            assignment.mtu,
		LogPrefix:      "execonnect: ",
	})
	if err != nil {
		return fmt.Errorf("create WireGuard device: %w", err)
	}
	if err := device.UpsertPeer(peer); err != nil {
		device.Close()
		return fmt.Errorf("configure WireGuard peer: %w", err)
	}
	oldDevice := tunnel.device
	tunnel.device = device
	tunnel.connectorAddress = assignment.connectorAddress
	tunnel.mtu = assignment.mtu
	tunnel.peerPublicKey = assignment.peerPublicKey
	if oldDevice != nil {
		oldDevice.Close()
	}
	return nil
}

func (tunnel *WireGuardTunnel) forceHandshake() error {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	if tunnel.device == nil || tunnel.peerPublicKey.IsZero() {
		return errors.New("WireGuard tunnel is unassigned")
	}
	if err := tunnel.device.ForceHandshake(tunnel.peerPublicKey); err != nil {
		return fmt.Errorf("force WireGuard handshake: %w", err)
	}
	return nil
}

// Net returns the assigned userspace network, or nil before the first assignment.
func (tunnel *WireGuardTunnel) Net() *nstun.Net {
	tunnel.mu.RLock()
	defer tunnel.mu.RUnlock()
	if tunnel.device == nil {
		return nil
	}
	return tunnel.device.Net()
}

// Close closes the active WireGuard device.
func (tunnel *WireGuardTunnel) Close() {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	if tunnel.device != nil {
		tunnel.device.Close()
		tunnel.device = nil
	}
}

func parseWireGuardAssignment(message *connectapi.WireGuardAssignment) (wireGuardAssignment, error) {
	if message == nil {
		return wireGuardAssignment{}, errors.New("WireGuard assignment is required")
	}
	connectorAddress, err := netip.ParseAddr(message.GetConnectorTunnelAddress())
	if err != nil || !connectorAddress.Is6() || !vpctunnel.TunnelAddressPrefix().Contains(connectorAddress) {
		return wireGuardAssignment{}, errors.New("invalid connector tunnel address")
	}
	if connectorAddress == vpctunnel.ServerTunnelAddress() {
		return wireGuardAssignment{}, errors.New("connector tunnel address is reserved for the server")
	}
	serverAddress, err := netip.ParseAddr(message.GetServerTunnelAddress())
	if err != nil || serverAddress != vpctunnel.ServerTunnelAddress() {
		return wireGuardAssignment{}, errors.New("invalid server tunnel address")
	}
	var peerPublicKey key.NodePublic
	if err := peerPublicKey.UnmarshalText([]byte(message.GetPeerPublicKey())); err != nil || peerPublicKey.IsZero() {
		return wireGuardAssignment{}, errors.New("invalid WireGuard peer public key")
	}
	peerEndpoint := message.GetPeerEndpoint()
	if peerEndpoint == "" || peerEndpoint != strings.TrimSpace(peerEndpoint) {
		return wireGuardAssignment{}, errors.New("invalid WireGuard peer endpoint")
	}
	host, portText, err := net.SplitHostPort(peerEndpoint)
	if err != nil || host == "" {
		return wireGuardAssignment{}, errors.New("invalid WireGuard peer endpoint")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return wireGuardAssignment{}, errors.New("invalid WireGuard peer endpoint")
	}
	if message.GetMtu() != uint32(vpctunnel.TunnelMTU()) {
		return wireGuardAssignment{}, errors.New("invalid WireGuard MTU")
	}
	return wireGuardAssignment{
		connectorAddress: connectorAddress,
		serverAddress:    serverAddress,
		peerPublicKey:    peerPublicKey,
		peerEndpoint:     peerEndpoint,
		mtu:              int(message.GetMtu()),
	}, nil
}
