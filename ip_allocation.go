package exe

import (
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/hashicorp/mdns"
)

type Allocation struct {
	IP       string
	Hostname string
}

type IPAllocator interface {
	// Allocate implementations should be idempotent: allocates an IP
	// address if there isn't one for this teamName, machineName already.
	// Otherwise, returns the already-allocated IP address.
	Allocate(teamName, machineName string) (*Allocation, error)
	Deallocate(teamName, machineName string) error
	Start() error
	Stop() error
	LookupMachine(teamName, ip string) (string, bool)
}

// MDNSAllocator uses multicast DNS to register hostnames of
// the form {machine-name}.{team-name}.exe.local with IP's
// allocated from a fixed-size pool of 127.0.0.x loopback addresses.
//
// Note that for MacOS: you may need to run ./cmd/setup-loopback first
// in order for the OS to recognize 127.0.0.x aliases since only 127.0.0.1
// exists by default.
//
// Note for Linux: I have not tested this at all on linux and I
// imagine it would take a fair amount of extra configuration before a
// stock container image could use this.
//
// Address ranges used:
//
//   - The hostname exe.local is assigned to 127.0.0.1, so that
//     http and ssh can Just Work using that hostname.
//
//   - machine.team.exe.local names (e.g. xray-alpha.banksean.exe.local) get
//     addresses in the range [127.0.0.2,  127.0.0.255] inclusive, such that
//     no single team uses the same IP address for more than one machine.
//     Multiple teams can use the same IP address, but these will point to
//     different machines.
type MDNSAllocator struct {
	// teamName -> (loopbackIP -> machineName)
	teamMachineMap map[string]map[string]string
	// Track which IPs are allocated to which teams
	// teamName -> []allocatedIPs
	teamIPAllocation map[string][]net.IP
	// Reverse lookup: loopbackIP string -> (teamName -> machineName)
	ipToMachine map[string]map[string]string
	// mDNS server instance
	httpPort       int
	httpMdnsServer *mdns.Server
	sshPort        int
	sshMdnsServer  *mdns.Server

	machineMdnsServers map[string]*mdns.Server

	// Thread safety
	mutex sync.RWMutex
}

var _ IPAllocator = &MDNSAllocator{}

func NewMDNSAllocator() *MDNSAllocator {
	return &MDNSAllocator{
		teamMachineMap:     make(map[string]map[string]string),
		teamIPAllocation:   make(map[string][]net.IP),
		ipToMachine:        make(map[string]map[string]string),
		httpPort:           8080,
		sshPort:            2222,
		machineMdnsServers: make(map[string]*mdns.Server),
	}
}

func (d *MDNSAllocator) Allocate(teamName, machineName string) (*Allocation, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	// Check if team already has too many machines (254 max)
	if len(d.teamIPAllocation[teamName]) >= 254 {
		return nil, fmt.Errorf("team %s has reached maximum of 254 machines", teamName)
	}

	// Check if this team already has an IP for this machine
	if teamMap, exists := d.teamMachineMap[teamName]; exists {
		for ip, existingMachineName := range teamMap {
			if existingMachineName == machineName {
				// Machine already has an IP, return it
				return &Allocation{
					IP:       ip,
					Hostname: fmt.Sprintf("%s.%s.exe.local", machineName, teamName),
				}, nil
			}
		}
	}

	// Find the next available IP for this team
	ip, err := d.findNextIPForTeam(teamName)
	if err != nil {
		return nil, err
	}

	// Update mappings
	if d.teamMachineMap[teamName] == nil {
		d.teamMachineMap[teamName] = make(map[string]string)
	}
	d.teamMachineMap[teamName][ip.String()] = machineName
	d.teamIPAllocation[teamName] = append(d.teamIPAllocation[teamName], ip)
	if _, ok := d.ipToMachine[ip.String()]; !ok {
		d.ipToMachine[ip.String()] = map[string]string{}
	}
	d.ipToMachine[ip.String()][teamName] = machineName

	// Register mDNS service: machineName.teamName.exe.local -> IP
	if err := d.registerServiceUnsafe(machineName, teamName, ip); err != nil {
		return nil, fmt.Errorf("failed to register mDNS service: %v", err)
	}

	slog.Info("Allocated IP for machine", "team", teamName, "machine", machineName, "ip", ip.String())

	return &Allocation{
		IP:       ip.String(),
		Hostname: fmt.Sprintf("%s.%s.exe.local", machineName, teamName),
	}, nil
}

func (d *MDNSAllocator) Deallocate(teamName, machineName string) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	// Find the IP for this machine
	var machineIP net.IP
	teamMap := d.teamMachineMap[teamName]
	if teamMap == nil {
		return nil // Team has no machines, nothing to deallocate
	}

	for ipStr, existingMachineName := range teamMap {
		if existingMachineName == machineName {
			machineIP = net.ParseIP(ipStr)
			break
		}
	}

	if machineIP == nil {
		return nil // Machine not found, nothing to deallocate
	}

	// Remove from mappings
	delete(d.teamMachineMap[teamName], machineIP.String())
	delete(d.ipToMachine, machineIP.String())

	// Remove IP from team allocation
	teamIPs := d.teamIPAllocation[teamName]
	for i, ip := range teamIPs {
		if ip.Equal(machineIP) {
			d.teamIPAllocation[teamName] = append(teamIPs[:i], teamIPs[i+1:]...)
			break
		}
	}
	machineKey := fmt.Sprintf("%s.%s", machineName, teamName)

	if server, exists := d.machineMdnsServers[machineKey]; exists {
		server.Shutdown()
		delete(d.machineMdnsServers, machineKey)
	}

	slog.Info("Deallocated IP for machine", "team", teamName, "machine", machineName, "ip", machineIP.String())
	return nil
}

// Start sets up the mdns servers to resolve exe.local and its subdomains.
func (d *MDNSAllocator) Start() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	localIP := net.IP{127, 0, 0, 1}

	// Create HTTP mDNS service for exe.local
	httpService, err := mdns.NewMDNSService(
		"exed-http",       // instance
		"_http._tcp",      // service
		"local.",          // domain (must end with dot)
		"exe.local.",      // hostname (must end with dot)
		d.httpPort,        // port
		[]net.IP{localIP}, // IPs
		[]string{"exe.dev local development server"}, // TXT
	)
	if err != nil {
		return fmt.Errorf("failed to create HTTP mDNS service: %w", err)
	}
	// Start the HTTP mDNS server
	d.httpMdnsServer, err = mdns.NewServer(&mdns.Config{Zone: httpService})
	if err != nil {
		return fmt.Errorf("failed to start HTTP mDNS server: %w", err)
	}

	// Create SSH mDNS service for exe.local
	sshService, err := mdns.NewMDNSService(
		"exed-ssh",                     // instance
		"_ssh._tcp",                    // service
		"local.",                       // domain (must end with dot)
		"exe.local.",                   // hostname (must end with dot)
		d.sshPort,                      // port
		[]net.IP{localIP},              // IPs
		[]string{"exe.dev SSH access"}, // TXT
	)
	if err != nil {
		return fmt.Errorf("failed to create SSH mDNS service: %w", err)
	}
	// Start the SSH mDNS server
	d.sshMdnsServer, err = mdns.NewServer(&mdns.Config{Zone: sshService})
	if err != nil {
		return fmt.Errorf("failed to start SSH mDNS server: %w", err)
	}

	slog.Info("Registered exe.local in mdns", "ip", localIP)
	return nil
}

func (d *MDNSAllocator) Stop() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.httpMdnsServer != nil {
		d.httpMdnsServer.Shutdown()
	}
	if d.sshMdnsServer != nil {
		d.sshMdnsServer.Shutdown()
	}
	for key, server := range d.machineMdnsServers {
		server.Shutdown()
		delete(d.machineMdnsServers, key)
	}

	slog.Info("mDNS servers stopped")
	return nil
}

func (d *MDNSAllocator) LookupMachine(teamName, ip string) (string, bool) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	teamMachines, exists := d.ipToMachine[ip]
	if !exists {
		return "", false
	}

	machineName, found := teamMachines[teamName]
	slog.Debug("LookupMachine", "teamName", teamName, "ip", ip, "found", found, "ipToMachine", d.ipToMachine)
	return machineName, found
}

// findNextIPForTeam finds the next available IP address for a team
func (d *MDNSAllocator) findNextIPForTeam(teamName string) (net.IP, error) {
	// Try all IPs in range [127.0.0.2, 127.0.0.255] to find one this team hasn't used
	for i := 2; i <= 255; i++ {
		candidateIP := net.IPv4(127, 0, 0, byte(i))

		// Check if this specific team is already using this IP
		teamAlreadyUsesIP := false
		if teamIPs := d.teamIPAllocation[teamName]; teamIPs != nil {
			for _, ip := range teamIPs {
				if ip.Equal(candidateIP) {
					teamAlreadyUsesIP = true
					break
				}
			}
		}

		// If this team hasn't used this IP, it's available
		// (Other teams may be using it, and that's fine)
		if !teamAlreadyUsesIP {
			return candidateIP, nil
		}
	}

	return nil, fmt.Errorf("team %s has reached maximum of 254 machines (127.0.0.2-127.0.0.255 range exhausted)", teamName)
}

// registerServiceUnsafe registers a mDNS service (caller must hold mutex)
func (d *MDNSAllocator) registerServiceUnsafe(machineName, teamName string, ip net.IP) error {
	// Construct hostname: machineName.teamName.exe.local or exe.local
	var hostname string
	if teamName == "" {
		hostname = fmt.Sprintf("%s.local", machineName)
	} else {
		hostname = fmt.Sprintf("%s.%s.exe.local", machineName, teamName)
	}
	machineKey := fmt.Sprintf("%s.%s", machineName, teamName)

	// Create mDNS service for this machine
	machineService, err := mdns.NewMDNSService(
		machineName+"-"+teamName, // instance
		"_ssh._tcp",              // service
		"local.",                 // domain
		hostname+".",             // hostname
		d.sshPort,                // port
		[]net.IP{ip},             // IP
		[]string{fmt.Sprintf("exe.dev machine %s in team %s", machineName, teamName)}, // TXT
	)
	if err != nil {
		return fmt.Errorf("failed to create machine mDNS service: %w", err)
	}

	// Start mDNS server for this machine
	machineServer, err := mdns.NewServer(&mdns.Config{Zone: machineService})
	if err != nil {
		return fmt.Errorf("failed to start machine mDNS server: %w", err)
	}

	d.machineMdnsServers[machineKey] = machineServer
	slog.Info(fmt.Sprintf("%s is at %s:%d", machineKey, ip, d.sshPort))
	return nil
}

type ProductionIPAllocator struct {
}

var _ IPAllocator = &ProductionIPAllocator{}

func NewProductionIPAllocator() *ProductionIPAllocator {
	return &ProductionIPAllocator{}
}

func (p *ProductionIPAllocator) Allocate(teamName, machineName string) (*Allocation, error) {
	return nil, fmt.Errorf("production IP allocation not yet implemented")
}

func (p *ProductionIPAllocator) Deallocate(teamName, machineName string) error {
	return fmt.Errorf("production IP deallocation not yet implemented")
}

func (p *ProductionIPAllocator) Start() error {
	return nil
}

func (p *ProductionIPAllocator) Stop() error {
	return nil
}

func (p *ProductionIPAllocator) LookupMachine(teamName, ip string) (string, bool) {
	return "", false
}
