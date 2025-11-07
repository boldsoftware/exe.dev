package types

import (
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ImageMetadata contains metadata information about an image
type ImageMetadata struct {
	// Digest is the content digest (e.g., sha256:abc123...)
	Digest string
	// MediaType is the media type of the root descriptor
	MediaType string
	// Size is the size in bytes of the root descriptor (manifest/index JSON)
	Size int64
	// ContentSize is the total compressed size of the image content (layers + config)
	// This is the download size from the registry, NOT the extracted filesystem size
	// For indexes, this is the sum of all platform manifests' compressed sizes
	// For single manifests, this is the compressed size (config + layer blobs)
	// Note: This will be smaller than what "docker images" shows (which is uncompressed)
	// To get uncompressed size, the layers must be downloaded and extracted
	ContentSize int64
	// Index contains the parsed index if the image is a multi-platform index
	Index *ocispec.Index
	// Manifest contains the parsed manifest if the image is a single manifest
	Manifest *ocispec.Manifest
	// Config contains the image configuration if available from a single manifest
	Config *ocispec.Image
}
