package container

import (
	"embed"
	"fmt"
	"io/fs"
	"runtime"
)

//go:embed rovol/**
var rovolFS embed.FS

// GetRovolFS returns the embedded filesystem for the specified architecture
func GetRovolFS(arch string) (fs.FS, error) {
	// Map common architecture names to our directory names
	switch arch {
	case "amd64", "x86_64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return nil, fmt.Errorf("unsupported architecture: %s", arch)
	}

	// Return a sub-filesystem for the specific architecture
	return fs.Sub(rovolFS, fmt.Sprintf("rovol/%s", arch))
}

// GetCurrentArchRovolFS returns the embedded filesystem for the current architecture
func GetCurrentArchRovolFS() (fs.FS, error) {
	return GetRovolFS(runtime.GOARCH)
}
