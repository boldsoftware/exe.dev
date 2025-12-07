package imageunpack

import (
	"os"
	"runtime"
)

// Config holds configuration for the image unpacker.
type Config struct {
	// Concurrency is the number of parallel HTTP range requests for downloading blobs.
	// Default: 10
	Concurrency int

	// ChunkSize is the size of each chunk to download in bytes.
	// Default: 8MB (8 * 1024 * 1024)
	ChunkSize int64

	// Username for registry authentication (optional).
	Username string

	// Password for registry authentication (optional).
	Password string

	// Insecure allows connecting to registries with invalid TLS certificates.
	Insecure bool

	// UseHTTP uses HTTP instead of HTTPS for registry connections.
	UseHTTP bool

	// NoSameOwner disables setting file ownership during extraction.
	// Required when running as non-root user.
	NoSameOwner bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Concurrency: 10,
		ChunkSize:   8 * 1024 * 1024, // 8MB
	}
}

// Platform returns the current platform in OS/arch format (e.g., "linux/amd64").
func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// IsRoot returns true if the current process is running as root (uid 0).
func IsRoot() bool {
	return os.Getuid() == 0
}
