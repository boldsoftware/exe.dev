package container

import (
	"slices"
	"strconv"
	"strings"
)

// buildEntrypointAndCmdArgs builds args to append after the image reference in nerdctl run.
// When useExetini is true, returns exetini args (e.g., -g -- ...) with the right command.
// Otherwise, returns command args or empty to use image defaults.
func buildEntrypointAndCmdArgs(useExetini bool, override string, imageEntrypoint, imageCmd []string) []string {
	// Normalize override values
	ov := strings.TrimSpace(override)

	// Helper to prefix tini flags when we have a command to run under exetini
	exetiniPrefix := func(cmdParts []string) []string {
		if !useExetini {
			return cmdParts
		}
		return append([]string{"-g", "--"}, cmdParts...)
	}

	// Custom override provided (and not auto/none)
	if ov != "" && ov != "auto" && ov != "none" {
		parts := strings.Fields(ov)
		return exetiniPrefix(parts)
	}

	if useExetini {
		// Prefer image entrypoint + cmd when available
		if len(imageEntrypoint) > 0 || len(imageCmd) > 0 {
			cmd := append([]string{}, imageEntrypoint...)
			cmd = append(cmd, imageCmd...)
			return exetiniPrefix(cmd)
		}
		// Fallback to keeping container alive
		return exetiniPrefix([]string{"sleep", "infinity"})
	}

	// Not using exetini
	if ov == "none" {
		// Ensure the container stays up to allow SSH setup
		return []string{"sleep", "infinity"}
	}

	// No override; rely on image defaults
	return nil
}

// ChooseBestPortToRoute returns the best port from exposedPorts for automatic routing.
// Priority: tcp/80 first, then smallest TCP port >= 1024.
func ChooseBestPortToRoute(exposedPorts map[string]struct{}) int {
	if len(exposedPorts) == 0 {
		return 0 // No exposed ports
	}

	// Check if tcp/80 is exposed
	if _, exists := exposedPorts["80/tcp"]; exists {
		return 80
	}

	var tcpPortsAbove1024 []int

	// Find all TCP ports >= 1024
	for portSpec := range exposedPorts {
		// Parse port specification (e.g., "8080/tcp", "5432/tcp")
		parts := strings.Split(portSpec, "/")
		if len(parts) != 2 || parts[1] != "tcp" {
			continue // Skip non-TCP ports
		}

		port, err := strconv.Atoi(parts[0])
		if err != nil {
			continue // Skip invalid ports
		}

		if port >= 1024 {
			tcpPortsAbove1024 = append(tcpPortsAbove1024, port)
		}
	}

	// Return smallest port >= 1024 if available
	if len(tcpPortsAbove1024) > 0 {
		return slices.Min(tcpPortsAbove1024)
	}

	return 0 // No TCP ports found
}
