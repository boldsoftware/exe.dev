package exeprox

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// setupProxyServers configures additional listeners for proxy ports.
// In staging/prod theere are thousands of these.
func (wp *WebProxy) setupProxyServers() {
	proxyPorts := wp.getProxyPorts()
	wp.proxyLns = make([]*listener, 0, len(proxyPorts))

	// Create listeners for each proxy port
	for _, port := range proxyPorts {
		addr := fmt.Sprintf(":%d", port)
		ln, err := startListener(wp.lg(), fmt.Sprintf("proxy-%d", port), addr)
		if err != nil {
			wp.lg().Warn("Failed to listen on proxy port, skipping", "port", port, "error", err)
			continue
		}

		wp.proxyLns = append(wp.proxyLns, ln)
	}

	// Log the ports. For small numbers of ports,
	// list them explicitly (for e1e tests).
	// For large numbers, show range (it's always contiguous in production).
	if len(wp.proxyLns) <= 10 {
		ports := make([]int, len(wp.proxyLns))
		for i, ln := range wp.proxyLns {
			ports[i] = ln.port()
		}
		wp.lg().Info("proxy listeners set up", "count", len(wp.proxyLns), "ports", ports)
	} else {
		wp.lg().Info("proxy listeners set up", "count", len(wp.proxyLns),
			"min_port", wp.proxyLns[0].port(),
			"max_port", wp.proxyLns[len(wp.proxyLns)-1].port())
	}
}

// getProxyPorts returns the list of ports that should be used for proxying.
// TEST_PROXY_PORTS env var overrides the stage config (used by e1e tests).
func (wp *WebProxy) getProxyPorts() []int {
	if testPorts := os.Getenv("TEST_PROXY_PORTS"); testPorts != "" {
		var ports []int
		for _, portStr := range strings.Split(testPorts, ",") {
			port, err := strconv.Atoi(portStr)
			if err != nil {
				wp.lg().Error("failed to parse TEST_PROXY_PORTS", "failed", portStr, "error", err, "env", testPorts)
				continue
			}
			ports = append(ports, port)
		}
		return ports
	}

	return wp.env.ProxyPorts
}
