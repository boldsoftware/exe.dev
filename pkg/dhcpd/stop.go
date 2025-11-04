package dhcpd

// Stop stops the DHCP server
func (s *DHCPServer) Stop() error {
	if s.srv != nil {
		s.srv.Close()
	}

	return nil
}
