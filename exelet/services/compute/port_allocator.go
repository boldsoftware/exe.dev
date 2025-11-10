package compute

import (
	"fmt"
	"sync"
)

const (
	minPort = 10000
	maxPort = 20000
)

// PortAllocator manages allocation of ports in a range
type PortAllocator struct {
	mu        sync.Mutex
	allocated map[int]bool
	nextPort  int
}

// NewPortAllocator creates a new port allocator
func NewPortAllocator() *PortAllocator {
	return &PortAllocator{
		allocated: make(map[int]bool),
		nextPort:  minPort,
	}
}

// Allocate allocates a port in the range minPort-maxPort
func (p *PortAllocator) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Try to find a free port starting from nextPort
	for i := 0; i < (maxPort - minPort); i++ {
		port := p.nextPort
		p.nextPort++
		if p.nextPort >= maxPort {
			p.nextPort = minPort
		}

		if !p.allocated[port] {
			p.allocated[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", minPort, maxPort)
}

// MarkAllocated marks a port as allocated (used when loading existing instances on startup)
func (p *PortAllocator) MarkAllocated(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allocated[port] = true
}

// Release releases a previously allocated port
func (p *PortAllocator) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allocated, port)
}
