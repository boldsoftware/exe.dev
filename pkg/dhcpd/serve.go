package dhcpd

import (
	"context"
	"fmt"
	"net"

	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
)

// Serve starts the DHCP server
func (s *DHCPServer) Serve(ctx context.Context) error {
	iface, err := netlink.LinkByName(s.config.Interface)
	if err != nil {
		return fmt.Errorf("unable to get DHCP interface: %w", err)
	}

	serverIP, err := s.getServerIP()
	if err != nil {
		return err
	}

	if err := s.ds.Reserve(iface.Attrs().HardwareAddr.String(), serverIP.String(), 0); err != nil {
		return err
	}

	laddr := net.UDPAddr{
		IP:   serverIP,
		Port: s.config.Port,
	}
	server, err := server4.NewServer(s.config.Interface, &laddr, s.handler)
	if err != nil {
		return err
	}
	s.srv = server

	errCh := make(chan error)
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)

		if err := server.Serve(); err != nil {
			errCh <- err
			return
		}
	}()

	// start prune
	go s.prune()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-doneCh:
	}

	return nil
}
