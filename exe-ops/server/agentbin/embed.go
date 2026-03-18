package agentbin

import "embed"

//go:embed binaries/*
var fs embed.FS

// Get returns the agent binary for the given OS and architecture.
// The binary is expected at binaries/{os}-{arch}/exe-ops-agent.
// Returns nil if no binary is available for the requested platform.
func Get(goos, goarch string) ([]byte, error) {
	path := "binaries/" + goos + "-" + goarch + "/exe-ops-agent"
	return fs.ReadFile(path)
}
