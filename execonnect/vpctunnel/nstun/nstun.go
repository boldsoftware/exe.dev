// SPDX-License-Identifier: MIT
// Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved.
//
// Adapted from tailscale/wireguard-go tun/netstack/tun.go at b48af7099cad.
// Local changes support the pinned gVisor API and TCP-only helpers.
// See ../../LICENSES/WireGuard-MIT.txt.

// Package nstun provides a small gVisor-backed WireGuard TUN with TCP helpers.
package nstun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"

	"github.com/tailscale/wireguard-go/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const nicID = 1

// Net is a netstack-backed WireGuard TUN with TCP dial and listen helpers.
type Net struct {
	endpoint       *channel.Endpoint
	stack          *stack.Stack
	events         chan tun.Event
	incomingPacket chan *buffer.View
	done           chan struct{}
	mtu            int
	hasIPv4        bool
	hasIPv6        bool
	closeOnce      sync.Once
}

// Create builds a netstack TUN with the given local addresses and MTU.
func Create(localAddresses []netip.Addr, mtu int) (tun.Device, *Net, error) {
	if mtu <= 0 {
		return nil, nil, errors.New("MTU must be positive")
	}
	network := &Net{
		endpoint: channel.New(1024, uint32(mtu), ""),
		stack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
			HandleLocal:        true,
		}),
		events:         make(chan tun.Event, 1),
		incomingPacket: make(chan *buffer.View),
		done:           make(chan struct{}),
		mtu:            mtu,
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			network.Close()
		}
	}()

	sackEnabled := tcpip.TCPSACKEnabled(true)
	if err := network.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &sackEnabled); err != nil {
		return nil, nil, fmt.Errorf("enable TCP SACK: %v", err)
	}
	network.endpoint.AddNotify(network)
	if err := network.stack.CreateNIC(nicID, network.endpoint); err != nil {
		return nil, nil, fmt.Errorf("create NIC: %v", err)
	}
	for _, address := range localAddresses {
		if !address.IsValid() {
			return nil, nil, errors.New("invalid local address")
		}
		protocol := ipv6.ProtocolNumber
		if address.Is4() {
			protocol = ipv4.ProtocolNumber
			network.hasIPv4 = true
		} else {
			network.hasIPv6 = true
		}
		protocolAddress := tcpip.ProtocolAddress{
			Protocol:          protocol,
			AddressWithPrefix: tcpip.AddrFromSlice(address.AsSlice()).WithPrefix(),
		}
		if err := network.stack.AddProtocolAddress(nicID, protocolAddress, stack.AddressProperties{}); err != nil {
			return nil, nil, fmt.Errorf("add local address %s: %v", address, err)
		}
	}
	if network.hasIPv4 {
		network.stack.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: nicID})
	}
	if network.hasIPv6 {
		network.stack.AddRoute(tcpip.Route{Destination: header.IPv6EmptySubnet, NIC: nicID})
	}
	network.events <- tun.EventUp
	closeOnError = false
	return network, network, nil
}

// Name returns the TUN device name.
func (n *Net) Name() (string, error) { return "nstun", nil }

// File reports that this userspace TUN has no backing file.
func (n *Net) File() *os.File { return nil }

// Events returns WireGuard TUN lifecycle events.
func (n *Net) Events() <-chan tun.Event { return n.events }

// MTU returns the configured maximum transmission unit.
func (n *Net) MTU() (int, error) { return n.mtu, nil }

// BatchSize returns the number of packets processed per read.
func (n *Net) BatchSize() int { return 1 }

// Read reads one outbound packet from the netstack for WireGuard.
func (n *Net) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-n.done:
		return 0, os.ErrClosed
	case view := <-n.incomingPacket:
		defer view.Release()
		count, err := view.Read(buffers[0][offset:])
		if err != nil {
			return 0, err
		}
		sizes[0] = count
		return 1, nil
	}
}

// Write injects decrypted WireGuard packets into the netstack.
func (n *Net) Write(buffers [][]byte, offset int) (int, error) {
	for _, packetBuffer := range buffers {
		packetBytes := packetBuffer[offset:]
		if len(packetBytes) == 0 {
			continue
		}
		packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(packetBytes)})
		switch packetBuffer[offset] >> 4 {
		case 4:
			n.endpoint.InjectInbound(header.IPv4ProtocolNumber, packet)
		case 6:
			n.endpoint.InjectInbound(header.IPv6ProtocolNumber, packet)
		default:
			packet.DecRef()
			return 0, syscall.EAFNOSUPPORT
		}
		packet.DecRef()
	}
	return len(buffers), nil
}

// WriteNotify moves one outbound netstack packet to the WireGuard reader.
func (n *Net) WriteNotify() {
	packet := n.endpoint.Read()
	if packet == nil {
		return
	}
	view := packet.ToView()
	packet.DecRef()
	select {
	case n.incomingPacket <- view:
	case <-n.done:
		view.Release()
	}
}

// Close closes the netstack TUN.
func (n *Net) Close() error {
	n.closeOnce.Do(func() {
		close(n.done)
		n.stack.RemoveNIC(nicID)
		n.endpoint.Close()
		n.stack.Close()
		close(n.events)
	})
	return nil
}

func fullAddress(address netip.AddrPort) (tcpip.FullAddress, tcpip.NetworkProtocolNumber) {
	protocol := ipv6.ProtocolNumber
	if address.Addr().Is4() {
		protocol = ipv4.ProtocolNumber
	}
	return tcpip.FullAddress{
		NIC:  nicID,
		Addr: tcpip.AddrFromSlice(address.Addr().AsSlice()),
		Port: address.Port(),
	}, protocol
}

// DialContextTCP dials a TCP connection through the netstack.
func (n *Net) DialContextTCP(ctx context.Context, address netip.AddrPort) (net.Conn, error) {
	fullAddress, protocol := fullAddress(address)
	return gonet.DialContextTCP(ctx, n.stack, fullAddress, protocol)
}

// ListenTCP listens for TCP connections on all configured local addresses.
func (n *Net) ListenTCP(port uint16) (net.Listener, error) {
	protocol := ipv6.ProtocolNumber
	if n.hasIPv4 && !n.hasIPv6 {
		protocol = ipv4.ProtocolNumber
	}
	return gonet.ListenTCP(n.stack, tcpip.FullAddress{NIC: nicID, Port: port}, protocol)
}
