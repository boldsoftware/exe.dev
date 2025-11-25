package dhcpd

import (
	"errors"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

func (s *DHCPServer) handler(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) {
	s.log.Debug("dhcp request",
		"hostname", m.HostName(),
		"broadcast", m.IsBroadcast(),
		"identifier", m.ClassIdentifier(),
		"type", m.MessageType(),
		"mac", m.ClientHWAddr.String(),
	)
	switch m.MessageType() {
	case dhcpv4.MessageTypeDiscover:
		if err := s.handleDiscover(conn, peer, m); err != nil {
			s.log.Error("error handling discover", "peer", peer.String())
		}
	case dhcpv4.MessageTypeRequest:
		if err := s.handleRequest(conn, peer, m); err != nil {
			s.log.Error("error handling request", "peer", peer.String())
		}
	case dhcpv4.MessageTypeRelease:
		if err := s.handleRelease(conn, peer, m); err != nil {
			s.log.Error("error handling release", "peer", peer.String())
		}
	}
}

func (s *DHCPServer) handleDiscover(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) error {
	serverIP, err := s.getServerIP()
	if err != nil {
		return err
	}

	// check for existing reservation
	lease, err := s.ds.Get(&Query{MACAddress: m.ClientHWAddr.String()})
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		// reserve
		ip, err := s.getNextIP()
		if err != nil {
			return err
		}
		lease = &Lease{
			MACAddress: m.ClientHWAddr.String(),
			IP:         ip.String(),
		}
	}

	clientIP := net.ParseIP(lease.IP)

	reply, err := dhcpv4.New(
		dhcpv4.WithYourIP(clientIP),
		dhcpv4.WithServerIP(serverIP),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer),
		dhcpv4.WithReply(m),
	)
	if err != nil {
		return err
	}

	reply.UpdateOption(dhcpv4.OptServerIdentifier(serverIP))

	s.log.Debug("sending reply", "peer", peer.String(), "reply", reply.String())

	if _, err := conn.WriteTo(reply.ToBytes(), peer); err != nil {
		return err
	}

	// reserve
	s.log.Debug("reserving IP",
		"ip", clientIP,
		"hostname", m.HostName(),
		"mac", m.ClientHWAddr.String(),
	)
	if err := s.ds.Reserve(m.ClientHWAddr.String(), clientIP.String()); err != nil {
		// if already exists; ignore
		if !errors.Is(err, ErrExists) {
			return err
		}
	}

	return nil
}

func (s *DHCPServer) handleRequest(conn net.PacketConn, peer net.Addr, m *dhcpv4.DHCPv4) error {
	clientLease, err := s.ds.Get(&Query{MACAddress: m.ClientHWAddr.String()})
	if err != nil {
		return err
	}

	clientIP := net.ParseIP(clientLease.IP)

	serverIP, err := s.getServerIP()
	if err != nil {
		return err
	}

	// dns
	dnsIPs := []net.IP{}
	for _, ip := range s.config.DNSServers {
		dnsIPs = append(dnsIPs, net.ParseIP(ip))
	}

	_, ipnet, err := net.ParseCIDR(s.config.Network)
	if err != nil {
		return err
	}

	reply, err := dhcpv4.New(
		dhcpv4.WithReply(m),
		dhcpv4.WithYourIP(clientIP),
		dhcpv4.WithServerIP(serverIP),
		dhcpv4.WithLeaseTime(uint32(leaseTTL.Seconds())),
		dhcpv4.WithGatewayIP(serverIP),
		dhcpv4.WithRouter(serverIP),
		dhcpv4.WithDNS(dnsIPs...),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(ipnet.Mask)),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
	)
	if err != nil {
		return err
	}

	s.log.Debug("sending reply",
		"peer", peer,
		"reply", reply,
	)

	if _, err := conn.WriteTo(reply.ToBytes(), peer); err != nil {
		return err
	}

	return nil
}

func (s *DHCPServer) handleRelease(_ net.PacketConn, _ net.Addr, m *dhcpv4.DHCPv4) error {
	if err := s.ds.Release(m.ClientIPAddr.String()); err != nil {
		return err
	}

	return nil
}
