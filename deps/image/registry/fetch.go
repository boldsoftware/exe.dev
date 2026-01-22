package registry

import (
	"context"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/deps/image/store"
	"exe.dev/deps/image/types"
)

// FetchDescriptor fetches an image descriptor and all its children.
// Deprecated: Use FetchDescriptorForPlatform to avoid downloading unnecessary platform layers.
func FetchDescriptor(resolver remotes.Resolver, memoryStore *store.ContentStore, imageRef reference.Named) (ocispec.Descriptor, error) {
	return Fetch(context.Background(), memoryStore, types.NewRequest(imageRef, "", allMediaTypes(), resolver), nil)
}

// FetchDescriptorForPlatform fetches an image descriptor and only the children matching the specified platform.
// The platform should be in "os/arch" format (e.g., "linux/amd64").
func FetchDescriptorForPlatform(ctx context.Context, resolver remotes.Resolver, memoryStore *store.ContentStore, imageRef reference.Named, platform string) (ocispec.Descriptor, error) {
	var matcher platforms.Matcher
	if platform != "" {
		p, err := platforms.Parse(platform)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		matcher = platforms.OnlyStrict(p)
	}
	return Fetch(ctx, memoryStore, types.NewRequest(imageRef, "", allMediaTypes(), resolver), matcher)
}

func allMediaTypes() []string {
	return []string{
		types.MediaTypeDockerSchema2Manifest,
		types.MediaTypeDockerSchema2ManifestList,
		ocispec.MediaTypeImageManifest,
		ocispec.MediaTypeImageIndex,
	}
}
