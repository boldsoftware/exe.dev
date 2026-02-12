package compute

import (
	"fmt"
	"math/rand/v2"
	"sync"
)

const (
	defaultMinPort = 10000
	defaultMaxPort = 20000
)

// PortAllocator manages allocation of ports in a range
type PortAllocator struct {
	mu        sync.Mutex
	allocated map[int]bool
	minPort   int
	maxPort   int
}

// NewPortAllocator creates a new port allocator with default port range
func NewPortAllocator() *PortAllocator {
	return NewPortAllocatorWithRange(defaultMinPort, defaultMaxPort)
}

// NewPortAllocatorWithRange creates a new port allocator with custom port range
func NewPortAllocatorWithRange(minPort, maxPort int) *PortAllocator {
	if minPort <= 0 || maxPort <= 0 || minPort >= maxPort {
		minPort = defaultMinPort
		maxPort = defaultMaxPort
	}
	return &PortAllocator{
		allocated: make(map[int]bool),
		minPort:   minPort,
		maxPort:   maxPort,
	}
}

// Allocate allocates a port in the configured range
func (p *PortAllocator) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Start from a random port in the range to avoid collisions between parallel allocators
	rangeSize := p.maxPort - p.minPort
	startOffset := rand.IntN(rangeSize)

	// Try to find a free port starting from the random position
	for i := range rangeSize {
		port := p.minPort + ((startOffset + i) % rangeSize)

		if !p.allocated[port] {
			p.allocated[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.minPort, p.maxPort)
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
