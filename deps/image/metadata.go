package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/deps/image/types"
	"exe.dev/deps/image/util"
)

// isImageManifest determines if a descriptor is an actual image manifest
// vs an attestation, signature, or other metadata
func isImageManifest(desc ocispec.Descriptor) bool {
	// Check media type - must be a manifest type
	switch desc.MediaType {
	case ocispec.MediaTypeImageManifest,
		types.MediaTypeDockerSchema2Manifest:
		// Valid image manifest type
	default:
		// Not an image manifest (might be attestation manifest, index, etc.)
		return false
	}

	// Check for attestation annotations
	if desc.Annotations != nil {
		// Attestations often have these annotations
		if _, ok := desc.Annotations["vnd.docker.reference.type"]; ok {
			return false
		}
		if refType, ok := desc.Annotations["org.opencontainers.image.ref.name"]; ok {
			// Attestations often reference things like "sha256-<hash>.att" or ".sig"
			if len(refType) > 4 && (refType[len(refType)-4:] == ".att" || refType[len(refType)-4:] == ".sig") {
				return false
			}
		}
	}

	// Must have a platform for an image manifest (attestations may not)
	if desc.Platform == nil {
		return false
	}

	// Valid image manifest
	return true
}

// FetchMetadata fetches only the root manifest/index metadata without pulling layers
func (i *ImageManager) FetchMetadata(ctx context.Context, ref string) (*types.ImageMetadata, error) {
	imageRef, err := util.ParseName(ref)
	if err != nil {
		return nil, err
	}

	// Set up registry resolver - this must happen before cache lookup to ensure
	// credentials are valid for this repository (prevents cross-repo cache attacks)
	resolver, err := util.NewResolver(imageRef, i.config.Username, i.config.Password, i.config.Insecure,
		i.config.UseHTTP, "", false)
	if err != nil {
		return nil, fmt.Errorf("error creating registry resolver: %w", err)
	}

	// Get the repository name (without tag/digest) for cache key
	repository := imageRef.Name()

	// For digest-based references, check cache after auth setup
	// Cache is keyed by repository+digest to prevent cross-repo data leaks
	if digested, ok := imageRef.(reference.Digested); ok {
		digestStr := digested.Digest().String()
		if cached, cacheErr := i.metadataCache.Get(repository, digestStr); cacheErr == nil {
			i.log.DebugContext(ctx, "using cached metadata for digest reference", "repository", repository, "digest", digestStr)
			return cached, nil
		}
	}

	// Resolve to get the descriptor
	name, desc, err := resolver.Resolve(ctx, imageRef.String())
	if err != nil {
		return nil, fmt.Errorf("error resolving image reference: %w", err)
	}

	i.log.DebugContext(ctx, "resolved image", "name", name, "digest", desc.Digest, "mediaType", desc.MediaType, "size", desc.Size)

	metadata := &types.ImageMetadata{
		Digest:    desc.Digest.String(),
		MediaType: desc.MediaType,
		Size:      desc.Size,
	}

	// Fetch the manifest/index content
	fetcher, err := resolver.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("error getting fetcher: %w", err)
	}

	rc, err := fetcher.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("error fetching descriptor: %w", err)
	}
	defer rc.Close()

	// Read the content
	content, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("error reading content: %w", err)
	}

	// Parse based on media type
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, types.MediaTypeDockerSchema2ManifestList:
		var idx ocispec.Index
		if err := json.Unmarshal(content, &idx); err != nil {
			return nil, fmt.Errorf("error unmarshaling index: %w", err)
		}
		metadata.Index = &idx
		i.log.DebugContext(ctx, "fetched index metadata", "manifests", len(idx.Manifests))

		// Calculate total compressed content size for all image manifests in the index
		// Skip non-image manifests (attestations, signatures, etc.)
		var totalContentSize int64
		var manifestCount int
		for _, manifestDesc := range idx.Manifests {
			// Only process actual image manifests, not attestations/signatures
			if !isImageManifest(manifestDesc) {
				i.log.DebugContext(ctx, "skipping non-image manifest",
					"digest", manifestDesc.Digest,
					"mediaType", manifestDesc.MediaType,
					"annotations", manifestDesc.Annotations)
				continue
			}

			// Fetch the manifest
			manifestRC, err := fetcher.Fetch(ctx, manifestDesc)
			if err != nil {
				return nil, fmt.Errorf("error fetching manifest %s: %w", manifestDesc.Digest, err)
			}

			manifestContent, err := io.ReadAll(manifestRC)
			manifestRC.Close()
			if err != nil {
				return nil, fmt.Errorf("error reading manifest content: %w", err)
			}

			var manifest ocispec.Manifest
			if err := json.Unmarshal(manifestContent, &manifest); err != nil {
				return nil, fmt.Errorf("error unmarshaling manifest: %w", err)
			}

			manifestSize := GetManifestSize(&manifest)
			totalContentSize += manifestSize
			manifestCount++

			platformStr := "unknown"
			if manifestDesc.Platform != nil {
				platformStr = fmt.Sprintf("%s/%s", manifestDesc.Platform.OS, manifestDesc.Platform.Architecture)
			}
			i.log.DebugContext(ctx, "counted manifest",
				"platform", platformStr,
				"digest", manifestDesc.Digest,
				"compressedSize", manifestSize)
		}
		metadata.ContentSize = totalContentSize
		i.log.DebugContext(ctx, "calculated index compressed content size", "totalSize", totalContentSize, "manifestCount", manifestCount)

	case ocispec.MediaTypeImageManifest, types.MediaTypeDockerSchema2Manifest:
		var manifest ocispec.Manifest
		if err := json.Unmarshal(content, &manifest); err != nil {
			return nil, fmt.Errorf("error unmarshaling manifest: %w", err)
		}
		metadata.Manifest = &manifest
		i.log.DebugContext(ctx, "fetched manifest metadata", "layers", len(manifest.Layers))

		// Calculate compressed content size
		metadata.ContentSize = GetManifestSize(&manifest)
		i.log.DebugContext(ctx, "calculated compressed content size", "size", metadata.ContentSize)

		// Fetch config for additional metadata
		if manifest.Config.Size > 0 {
			configRC, err := fetcher.Fetch(ctx, manifest.Config)
			if err != nil {
				return nil, fmt.Errorf("error fetching config: %w", err)
			}
			defer configRC.Close()

			configContent, err := io.ReadAll(configRC)
			if err != nil {
				return nil, fmt.Errorf("error reading config: %w", err)
			}

			var conf ocispec.Image
			if err := json.Unmarshal(configContent, &conf); err != nil {
				return nil, fmt.Errorf("error unmarshaling config: %w", err)
			}
			metadata.Config = &conf
		}
	default:
		return nil, fmt.Errorf("unsupported media type: %s", desc.MediaType)
	}

	// Cache metadata by repository and digest for future lookups
	_ = i.metadataCache.Put(repository, metadata.Digest, metadata)

	return metadata, nil
}

// FetchManifestForPlatform fetches metadata for a specific platform from a multi-platform index
func (i *ImageManager) FetchManifestForPlatform(ctx context.Context, ref, platform string) (*types.ImageMetadata, error) {
	// First fetch the root metadata
	rootMetadata, err := i.FetchMetadata(ctx, ref)
	if err != nil {
		return nil, err
	}

	// If it's not an index, return the root metadata
	if rootMetadata.Index == nil {
		return rootMetadata, nil
	}

	// Find the platform-specific manifest
	imageRef, err := util.ParseName(ref)
	if err != nil {
		return nil, err
	}

	resolver, err := util.NewResolver(imageRef, i.config.Username, i.config.Password, i.config.Insecure,
		i.config.UseHTTP, "", false)
	if err != nil {
		return nil, fmt.Errorf("error creating registry resolver: %w", err)
	}

	name, _, err := resolver.Resolve(ctx, imageRef.String())
	if err != nil {
		return nil, fmt.Errorf("error resolving image reference: %w", err)
	}

	fetcher, err := resolver.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("error getting fetcher: %w", err)
	}

	// Find matching platform
	for _, desc := range rootMetadata.Index.Manifests {
		if desc.Platform != nil {
			platformStr := fmt.Sprintf("%s/%s", desc.Platform.OS, desc.Platform.Architecture)
			if platformStr == platform {
				i.log.DebugContext(ctx, "found platform manifest", "platform", platform, "digest", desc.Digest)

				// Fetch this specific manifest
				rc, err := fetcher.Fetch(ctx, desc)
				if err != nil {
					return nil, fmt.Errorf("error fetching platform manifest: %w", err)
				}
				defer rc.Close()

				content, err := io.ReadAll(rc)
				if err != nil {
					return nil, fmt.Errorf("error reading manifest content: %w", err)
				}

				var manifest ocispec.Manifest
				if err := json.Unmarshal(content, &manifest); err != nil {
					return nil, fmt.Errorf("error unmarshaling manifest: %w", err)
				}

				// Calculate compressed size
				contentSize := GetManifestSize(&manifest)
				i.log.DebugContext(ctx, "calculated platform manifest compressed size", "platform", platform, "size", contentSize)

				// Fetch config for additional metadata
				var conf *ocispec.Image
				if manifest.Config.Size > 0 {
					configRC, err := fetcher.Fetch(ctx, manifest.Config)
					if err != nil {
						return nil, fmt.Errorf("error fetching config: %w", err)
					}
					defer configRC.Close()

					configContent, err := io.ReadAll(configRC)
					if err != nil {
						return nil, fmt.Errorf("error reading config: %w", err)
					}

					var c ocispec.Image
					if err := json.Unmarshal(configContent, &c); err != nil {
						return nil, fmt.Errorf("error unmarshaling config: %w", err)
					}
					conf = &c
				}

				platformMetadata := &types.ImageMetadata{
					Digest:      desc.Digest.String(),
					MediaType:   desc.MediaType,
					Size:        desc.Size,
					ContentSize: contentSize,
					Manifest:    &manifest,
					Config:      conf,
				}

				// Cache platform-specific metadata by repository and digest
				_ = i.metadataCache.Put(imageRef.Name(), platformMetadata.Digest, platformMetadata)

				return platformMetadata, nil
			}
		}
	}

	return nil, fmt.Errorf("no manifest found for platform: %s", platform)
}

// FetchConfig fetches the OCI image configuration without downloading layers
// For multi-platform images, platform must be specified (e.g., "linux/amd64")
func (i *ImageManager) FetchConfig(ctx context.Context, ref, platform string) (*ocispec.Image, error) {
	// First fetch the root metadata
	rootMetadata, err := i.FetchMetadata(ctx, ref)
	if err != nil {
		return nil, err
	}

	var manifest *ocispec.Manifest

	// Determine if we need to fetch a platform-specific manifest
	if rootMetadata.Index != nil {
		// Multi-platform index - need to fetch the specific platform manifest
		if platform == "" {
			return nil, fmt.Errorf("platform must be specified for multi-platform image")
		}

		platformMetadata, err := i.FetchManifestForPlatform(ctx, ref, platform)
		if err != nil {
			return nil, err
		}
		manifest = platformMetadata.Manifest
	} else if rootMetadata.Manifest != nil {
		// Single manifest
		manifest = rootMetadata.Manifest
	} else {
		return nil, fmt.Errorf("no manifest found in image metadata")
	}

	if manifest.Config.Size == 0 {
		return nil, fmt.Errorf("manifest does not contain a config descriptor")
	}

	// Fetch the config blob
	imageRef, err := util.ParseName(ref)
	if err != nil {
		return nil, err
	}

	resolver, err := util.NewResolver(imageRef, i.config.Username, i.config.Password, i.config.Insecure,
		i.config.UseHTTP, "", false)
	if err != nil {
		return nil, fmt.Errorf("error creating registry resolver: %w", err)
	}

	name, _, err := resolver.Resolve(ctx, imageRef.String())
	if err != nil {
		return nil, fmt.Errorf("error resolving image reference: %w", err)
	}

	fetcher, err := resolver.Fetcher(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("error getting fetcher: %w", err)
	}

	i.log.DebugContext(ctx, "fetching image config", "digest", manifest.Config.Digest, "size", manifest.Config.Size)

	configRC, err := fetcher.Fetch(ctx, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("error fetching config: %w", err)
	}
	defer configRC.Close()

	configContent, err := io.ReadAll(configRC)
	if err != nil {
		return nil, fmt.Errorf("error reading config: %w", err)
	}

	var conf ocispec.Image
	if err := json.Unmarshal(configContent, &conf); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	i.log.DebugContext(ctx, "fetched image config", "arch", conf.Architecture, "os", conf.OS)

	return &conf, nil
}

// GetManifestSize calculates the total compressed size of a manifest (config + all layers)
// This returns the size of the compressed blobs as stored in the registry
// This does not require downloading the actual content, just reading the descriptor sizes
func GetManifestSize(manifest *ocispec.Manifest) int64 {
	if manifest == nil {
		return 0
	}

	var totalSize int64

	// Add config size
	totalSize += manifest.Config.Size

	// Add all layer sizes (compressed)
	for _, layer := range manifest.Layers {
		totalSize += layer.Size
	}

	return totalSize
}

// GetIndexTotalSize calculates the total size of all image manifests in an index
// This sums up the sizes of all platform-specific image manifests (excludes attestations)
func (i *ImageManager) GetIndexTotalSize(ctx context.Context, ref string) (int64, error) {
	metadata, err := i.FetchMetadata(ctx, ref)
	if err != nil {
		return 0, err
	}

	// FetchMetadata already calculates ContentSize with proper filtering
	return metadata.ContentSize, nil
}

// GetImageSize calculates the total size of an image without pulling the content
// For multi-platform images, it returns the size of the specific platform manifest
// For single-platform images, it returns the manifest size
func (i *ImageManager) GetImageSize(ctx context.Context, ref, platform string) (int64, error) {
	metadata, err := i.FetchMetadata(ctx, ref)
	if err != nil {
		return 0, err
	}

	// If it's a single manifest, return its size
	if metadata.Manifest != nil {
		return GetManifestSize(metadata.Manifest), nil
	}

	// If it's an index and platform is specified, get that platform's size
	if metadata.Index != nil && platform != "" {
		platformMetadata, err := i.FetchManifestForPlatform(ctx, ref, platform)
		if err != nil {
			return 0, err
		}
		return GetManifestSize(platformMetadata.Manifest), nil
	}

	// If it's an index but no platform specified, require platform
	if metadata.Index != nil {
		return 0, fmt.Errorf("platform must be specified for multi-platform image")
	}

	return 0, fmt.Errorf("image has neither manifest nor index")
}
