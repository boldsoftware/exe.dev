package dhcpd

// Config is the DHCPServer configuration
type Config struct {
	// DataDir is the directory for DHCPServer state
	DataDir string
	// Interface is the network interface on which the DHCPServer listens
	Interface string
	// Network is the CIDR subnet for DHCP leases
	Network string
	// Port is the port to listen
	Port int
	// DNSServers is a slice of DNS servers to issue to clients
	DNSServers []string
}
