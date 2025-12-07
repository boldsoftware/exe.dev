package imageunpack

import (
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Result contains the result of unpacking an image.
type Result struct {
	// Digest is the image manifest digest (e.g., "sha256:abc123...").
	Digest string

	// Config is the parsed OCI image configuration.
	Config *ocispec.Image

	// UnpackedSize is the total size of unpacked content in bytes.
	UnpackedSize int64

	// CompressedSize is the total size of compressed layers in bytes.
	CompressedSize int64

	// LayerCount is the number of layers unpacked.
	LayerCount int
}
