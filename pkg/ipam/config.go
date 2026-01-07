package ipam

// Config is the IPAM configuration
type Config struct {
	// DataDir is the directory for IPAM state
	DataDir string
	// Network is the CIDR subnet for IP leases
	Network string
}
