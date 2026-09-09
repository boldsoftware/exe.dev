// Package wgnet provides reusable userspace WireGuard devices.
package wgnet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boldsoftware/exe.dev/execonnect/vpctunnel/nstun"

	"github.com/tailscale/wireguard-go/conn"
	"github.com/tailscale/wireguard-go/device"
	"tailscale.com/types/key"
)

// Config configures a userspace WireGuard device.
type Config struct {
	// PrivateKey is the local WireGuard node private key.
	PrivateKey key.NodePrivate
	// LocalAddresses are assigned to the userspace network.
	LocalAddresses []netip.Addr
	// MTU is the userspace network's maximum transmission unit.
	MTU int
	// ListenPort is the local UDP port, or zero for an ephemeral port.
	ListenPort int
	// LogPrefix prefixes wireguard-go error messages.
	LogPrefix string
	// Bind is the UDP transport, or nil to use wireguard-go's default bind.
	// The device owns the bind and closes it when it stops.
	Bind conn.Bind
}

// Peer describes the desired state of a WireGuard peer.
type Peer struct {
	// PublicKey authenticates the peer.
	PublicKey key.NodePublic
	// Endpoint is the peer's UDP host and port, or empty if it is learned dynamically.
	Endpoint string
	// AllowedIPs are the prefixes routed to the peer.
	AllowedIPs []netip.Prefix
	// PersistentKeepalive sends an authenticated packet at this interval. Zero disables it.
	PersistentKeepalive time.Duration
}

// Device is a running userspace WireGuard device and network.
type Device struct {
	device    *device.Device
	network   *nstun.Net
	publicKey key.NodePublic
	peerMu    sync.Mutex
}

// New creates and starts a peer-free userspace WireGuard device.
func New(config Config) (*Device, error) {
	if config.PrivateKey.IsZero() {
		return nil, errors.New("private key must not be zero")
	}
	if config.MTU <= 0 {
		return nil, errors.New("MTU must be positive")
	}
	if config.ListenPort < 0 || config.ListenPort > 65535 {
		return nil, errors.New("listen port must be between 0 and 65535")
	}

	tunDevice, network, err := nstun.Create(config.LocalAddresses, config.MTU)
	if err != nil {
		return nil, fmt.Errorf("create userspace TUN: %w", err)
	}
	bind := config.Bind
	if bind == nil {
		bind = conn.NewDefaultBind()
	}
	wireGuardDevice := device.NewDevice(
		tunDevice,
		bind,
		device.NewLogger(device.LogLevelError, config.LogPrefix),
	)
	result := &Device{
		device:    wireGuardDevice,
		network:   network,
		publicKey: config.PrivateKey.Public(),
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			result.Close()
		}
	}()

	if err := wireGuardDevice.SetPrivateKey(device.NoisePrivateKey(config.PrivateKey.Raw32())); err != nil {
		return nil, fmt.Errorf("set WireGuard private key: %w", err)
	}
	// wireguard-go does not expose a concrete listen-port setter.
	if err := wireGuardDevice.IpcSet(fmt.Sprintf("listen_port=%d\n", config.ListenPort)); err != nil {
		return nil, fmt.Errorf("set WireGuard listen port: %w", err)
	}
	if err := wireGuardDevice.Up(); err != nil {
		return nil, fmt.Errorf("start WireGuard device: %w", err)
	}
	closeOnError = false
	return result, nil
}

// PublicKey returns the local WireGuard node public key.
func (d *Device) PublicKey() key.NodePublic {
	return d.publicKey
}

// Net returns the userspace network attached to the device.
func (d *Device) Net() *nstun.Net {
	return d.network
}

// ListenPort returns the UDP port used by the WireGuard device.
func (d *Device) ListenPort() (uint16, error) {
	configuration, err := d.device.IpcGet()
	if err != nil {
		return 0, fmt.Errorf("read WireGuard configuration: %w", err)
	}
	for line := range strings.SplitSeq(configuration, "\n") {
		value, ok := strings.CutPrefix(line, "listen_port=")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("parse WireGuard listen port: %w", err)
		}
		return uint16(port), nil
	}
	return 0, errors.New("WireGuard listen port is unavailable")
}

// UpsertPeer adds a peer or replaces its endpoint and allowed IPs in place.
func (d *Device) UpsertPeer(config Peer) error {
	if config.PublicKey.IsZero() {
		return errors.New("public key must not be zero")
	}
	allowedIPs := make([]netip.Prefix, len(config.AllowedIPs))
	for index, prefix := range config.AllowedIPs {
		if !prefix.IsValid() {
			return fmt.Errorf("allowed IP %d is invalid", index)
		}
		allowedIPs[index] = prefix.Masked()
	}
	endpoint, err := parseEndpoint(d.device.Bind(), config.Endpoint)
	if err != nil {
		return err
	}
	if config.PersistentKeepalive < 0 || config.PersistentKeepalive%time.Second != 0 || config.PersistentKeepalive > 65535*time.Second {
		return errors.New("persistent keepalive must be whole seconds between 0 and 65535")
	}

	publicKey := device.NoisePublicKey(config.PublicKey.Raw32())
	d.peerMu.Lock()
	defer d.peerMu.Unlock()
	wireGuardPeer, ok := d.device.LookupActivePeer(publicKey)
	if !ok {
		wireGuardPeer, err = d.device.NewPeer(publicKey)
		if err != nil {
			return fmt.Errorf("create WireGuard peer: %w", err)
		}
	}
	wireGuardPeer.SetAllowedIPs(allowedIPs)
	wireGuardPeer.SetEndpointFromPacket(endpoint)
	wireGuardPeer.Start()
	publicKeyBytes := config.PublicKey.Raw32()
	if err := d.device.IpcSet(fmt.Sprintf(
		"public_key=%s\npersistent_keepalive_interval=%d\n",
		hex.EncodeToString(publicKeyBytes[:]),
		config.PersistentKeepalive/time.Second,
	)); err != nil {
		return fmt.Errorf("set persistent keepalive: %w", err)
	}
	return nil
}

// ForceHandshake expires a peer's session and starts a fresh handshake.
func (d *Device) ForceHandshake(publicKey key.NodePublic) error {
	if publicKey.IsZero() {
		return errors.New("public key must not be zero")
	}
	d.peerMu.Lock()
	defer d.peerMu.Unlock()
	peer, ok := d.device.LookupActivePeer(device.NoisePublicKey(publicKey.Raw32()))
	if !ok {
		return errors.New("WireGuard peer not found")
	}
	peer.ExpireCurrentKeypairs()
	if err := peer.SendHandshakeInitiation(false); err != nil {
		return fmt.Errorf("send WireGuard handshake initiation: %w", err)
	}
	return nil
}

// RemovePeer removes a peer if it exists.
func (d *Device) RemovePeer(publicKey key.NodePublic) {
	d.peerMu.Lock()
	defer d.peerMu.Unlock()
	d.device.RemovePeer(device.NoisePublicKey(publicKey.Raw32()))
}

// PeerTransferBytes returns the peer's authenticated receive- and transmit-byte counters.
func (d *Device) PeerTransferBytes(publicKey key.NodePublic) (uint64, uint64, bool, error) {
	configuration, err := d.device.IpcGet()
	if err != nil {
		return 0, 0, false, fmt.Errorf("read WireGuard configuration: %w", err)
	}
	return parsePeerTransferBytes(configuration, publicKey)
}

// PeerReceiveBytes returns the peer's authenticated receive-byte counter.
func (d *Device) PeerReceiveBytes(publicKey key.NodePublic) (uint64, bool, error) {
	receiveBytes, _, found, err := d.PeerTransferBytes(publicKey)
	return receiveBytes, found, err
}

func parsePeerTransferBytes(configuration string, publicKey key.NodePublic) (uint64, uint64, bool, error) {
	raw := publicKey.Raw32()
	wanted := hex.EncodeToString(raw[:])
	matched := false
	var receiveBytes, transmitBytes uint64
	var foundReceiveBytes, foundTransmitBytes bool
	for line := range strings.SplitSeq(configuration, "\n") {
		if value, ok := strings.CutPrefix(line, "public_key="); ok {
			if matched {
				break
			}
			matched = value == wanted
			continue
		}
		if !matched {
			continue
		}
		if value, ok := strings.CutPrefix(line, "rx_bytes="); ok {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, 0, false, fmt.Errorf("parse WireGuard receive bytes: %w", err)
			}
			receiveBytes = parsed
			foundReceiveBytes = true
		}
		if value, ok := strings.CutPrefix(line, "tx_bytes="); ok {
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, 0, false, fmt.Errorf("parse WireGuard transmit bytes: %w", err)
			}
			transmitBytes = parsed
			foundTransmitBytes = true
		}
	}
	if !matched || !foundReceiveBytes || !foundTransmitBytes {
		return 0, 0, false, nil
	}
	return receiveBytes, transmitBytes, true, nil
}

func parseEndpoint(bind conn.Bind, endpoint string) (conn.Endpoint, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	address, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP endpoint %q: %w", endpoint, err)
	}
	if address.IP == nil {
		return nil, fmt.Errorf("resolve UDP endpoint %q: no IP address", endpoint)
	}
	parsed, err := bind.ParseEndpoint(address.String())
	if err != nil {
		return nil, fmt.Errorf("parse UDP endpoint %q: %w", endpoint, err)
	}
	return parsed, nil
}

// Close closes the WireGuard device and its userspace network.
func (d *Device) Close() {
	if d != nil && d.device != nil {
		d.device.Close()
	}
}
