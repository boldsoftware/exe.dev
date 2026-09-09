package wgnet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/wireguard-go/conn"
	"tailscale.com/types/key"
)

type failingBind struct {
	conn.Bind
	err error
}

func (bind failingBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	return nil, 0, bind.err
}

func TestDeviceUsesConfiguredBind(t *testing.T) {
	want := errors.New("test bind cannot open")
	device, err := New(Config{
		PrivateKey:     key.NewNode(),
		LocalAddresses: []netip.Addr{netip.MustParseAddr("fd65:7865::1")},
		MTU:            1280,
		Bind:           failingBind{Bind: conn.NewDefaultBind(), err: want},
	})
	if device != nil {
		device.Close()
		t.Fatal("device started despite bind failure")
	}
	if !errors.Is(err, want) {
		t.Fatalf("New error = %v, want %v", err, want)
	}
}

func TestDeviceLifecycle(t *testing.T) {
	privateKey := key.NewNode()
	device, err := New(Config{
		PrivateKey:     privateKey,
		LocalAddresses: []netip.Addr{netip.MustParseAddr("fd65:7865::1")},
		MTU:            1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()

	if device.PublicKey() != privateKey.Public() {
		t.Fatalf("public key = %s, want %s", device.PublicKey(), privateKey.Public())
	}
	if device.Net() == nil {
		t.Fatal("network is nil")
	}
	port, err := device.ListenPort()
	if err != nil {
		t.Fatal(err)
	}
	if port == 0 {
		t.Fatal("listen port is zero")
	}
}

func TestUpsertAndRemovePeer(t *testing.T) {
	device := newTestDevice(t)
	peerKey := key.NewNode().Public()

	if err := device.UpsertPeer(Peer{
		PublicKey:  peerKey,
		Endpoint:   "127.0.0.1:12345",
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("fd65:7865:0:1::1/128")},
	}); err != nil {
		t.Fatal(err)
	}
	configuration := wireGuardConfiguration(t, device)
	if !strings.Contains(configuration, "endpoint=127.0.0.1:12345") {
		t.Fatalf("configuration missing endpoint: %s", configuration)
	}
	if !strings.Contains(configuration, "allowed_ip=fd65:7865:0:1::1/128") {
		t.Fatalf("configuration missing allowed IP: %s", configuration)
	}
	if err := device.UpsertPeer(Peer{
		PublicKey:  peerKey,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("fd65:7865:0:2::/64")},
	}); err != nil {
		t.Fatal(err)
	}
	configuration = wireGuardConfiguration(t, device)
	if strings.Contains(configuration, "endpoint=127.0.0.1:12345") {
		t.Fatalf("configuration retained old endpoint: %s", configuration)
	}
	if strings.Contains(configuration, "allowed_ip=fd65:7865:0:1::1/128") {
		t.Fatalf("configuration retained old allowed IP: %s", configuration)
	}
	if !strings.Contains(configuration, "allowed_ip=fd65:7865:0:2::/64") {
		t.Fatalf("configuration missing updated allowed IP: %s", configuration)
	}
	device.RemovePeer(peerKey)
	if strings.Contains(wireGuardConfiguration(t, device), peerKey.UntypedHexString()) {
		t.Fatal("configuration retained removed peer")
	}
}

func TestTwoDevicesCarryTCP(t *testing.T) {
	serverIP := netip.MustParseAddr("fd65:7865::1")
	clientIP := netip.MustParseAddr("fd65:7865:0:1::1")
	serverKey := key.NewNode()
	clientKey := key.NewNode()
	server := newTestDeviceWithConfig(t, serverKey, serverIP)
	client := newTestDeviceWithConfig(t, clientKey, clientIP)

	serverPort, err := server.ListenPort()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.UpsertPeer(Peer{
		PublicKey:  clientKey.Public(),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientIP, 128)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertPeer(Peer{
		PublicKey:  serverKey.Public(),
		Endpoint:   net.JoinHostPort("127.0.0.1", fmt.Sprint(serverPort)),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(serverIP, 128)},
	}); err != nil {
		t.Fatal(err)
	}

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
	connection, err := client.Net().DialContextTCP(ctx, netip.AddrPortFrom(serverIP, 4700))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}

	const payload = "through WireGuard"
	if _, err := io.WriteString(connection, payload); err != nil {
		t.Fatal(err)
	}
	closeWriter, ok := connection.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("connection type %T does not support CloseWrite", connection)
	}
	if err := closeWriter.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestForceHandshakeRecoversAfterResponderRestart(t *testing.T) {
	serverIP := netip.MustParseAddr("fd65:7865::1")
	clientIP := netip.MustParseAddr("fd65:7865:0:1::1")
	serverKey := key.NewNode()
	clientKey := key.NewNode()
	server, err := New(Config{PrivateKey: serverKey, LocalAddresses: []netip.Addr{serverIP}, MTU: 1280})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverPort, err := server.ListenPort()
	if err != nil {
		t.Fatal(err)
	}
	client := newTestDeviceWithConfig(t, clientKey, clientIP)
	configureTestPeers(t, server, client, serverKey.Public(), clientKey.Public(), serverIP, clientIP, serverPort)
	assertTestDeviceTCP(t, client, server, serverIP, "before restart")

	server.Close()
	replacement, err := New(Config{PrivateKey: serverKey, LocalAddresses: []netip.Addr{serverIP}, MTU: 1280, ListenPort: int(serverPort)})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := replacement.UpsertPeer(Peer{PublicKey: clientKey.Public(), AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientIP, 128)}}); err != nil {
		t.Fatal(err)
	}
	if err := client.ForceHandshake(serverKey.Public()); err != nil {
		t.Fatal(err)
	}
	assertTestDeviceTCP(t, client, replacement, serverIP, "after restart")
}

func configureTestPeers(t *testing.T, server, client *Device, serverKey, clientKey key.NodePublic, serverIP, clientIP netip.Addr, serverPort uint16) {
	t.Helper()
	if err := server.UpsertPeer(Peer{PublicKey: clientKey, AllowedIPs: []netip.Prefix{netip.PrefixFrom(clientIP, 128)}}); err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertPeer(Peer{
		PublicKey:  serverKey,
		Endpoint:   net.JoinHostPort("127.0.0.1", fmt.Sprint(serverPort)),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(serverIP, 128)},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertTestDeviceTCP(t *testing.T, client, server *Device, serverIP netip.Addr, payload string) {
	t.Helper()
	listener, err := server.Net().ListenTCP(4701)
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

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	connection, err := client.Net().DialContextTCP(ctx, netip.AddrPortFrom(serverIP, 4701))
	if err != nil {
		t.Fatal(err)
	}
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

func TestUpsertPeerRejectsInvalidConfig(t *testing.T) {
	device := newTestDevice(t)
	peerKey := key.NewNode().Public()
	tests := []struct {
		name string
		peer Peer
	}{
		{name: "zero public key", peer: Peer{}},
		{name: "invalid endpoint", peer: Peer{PublicKey: peerKey, Endpoint: "not an endpoint"}},
		{name: "invalid allowed IP", peer: Peer{PublicKey: peerKey, AllowedIPs: []netip.Prefix{{}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := device.UpsertPeer(test.peer); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "zero private key", config: Config{MTU: 1280}},
		{name: "zero MTU", config: Config{PrivateKey: key.NewNode()}},
		{name: "negative listen port", config: Config{PrivateKey: key.NewNode(), MTU: 1280, ListenPort: -1}},
		{name: "large listen port", config: Config{PrivateKey: key.NewNode(), MTU: 1280, ListenPort: 65536}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func newTestDevice(t *testing.T) *Device {
	t.Helper()
	return newTestDeviceWithConfig(t, key.NewNode(), netip.MustParseAddr("fd65:7865::1"))
}

func newTestDeviceWithConfig(t *testing.T, privateKey key.NodePrivate, address netip.Addr) *Device {
	t.Helper()
	device, err := New(Config{
		PrivateKey:     privateKey,
		LocalAddresses: []netip.Addr{address},
		MTU:            1280,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(device.Close)
	return device
}

func wireGuardConfiguration(t *testing.T, device *Device) string {
	t.Helper()
	configuration, err := device.device.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func TestParsePeerTransferBytes(t *testing.T) {
	privateKey := key.NewNode()
	publicKey := privateKey.Public()
	raw := publicKey.Raw32()
	configuration := "private_key=00\npublic_key=" + hex.EncodeToString(raw[:]) + "\ntx_bytes=7\nrx_bytes=42\n"
	received, transmitted, found, err := parsePeerTransferBytes(configuration, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !found || received != 42 || transmitted != 7 {
		t.Fatalf("transfer bytes = (%d, %d), found=%v", received, transmitted, found)
	}
	if _, _, found, err := parsePeerTransferBytes(configuration, key.NewNode().Public()); err != nil || found {
		t.Fatalf("missing peer found=%v err=%v", found, err)
	}
}

func TestParsePeerTransferBytesRejectsInvalidCounters(t *testing.T) {
	publicKey := key.NewNode().Public()
	raw := publicKey.Raw32()
	prefix := "public_key=" + hex.EncodeToString(raw[:]) + "\n"
	for _, test := range []struct {
		name          string
		configuration string
	}{
		{name: "receive", configuration: prefix + "rx_bytes=invalid\ntx_bytes=7\n"},
		{name: "transmit", configuration: prefix + "rx_bytes=42\ntx_bytes=invalid\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := parsePeerTransferBytes(test.configuration, publicKey); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
