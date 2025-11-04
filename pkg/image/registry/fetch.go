package registry

import (
	"context"

	"github.com/containerd/containerd/remotes"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/pkg/image/store"
	"exe.dev/pkg/image/types"
)

func FetchDescriptor(resolver remotes.Resolver, memoryStore *store.ContentStore, imageRef reference.Named) (ocispec.Descriptor, error) {
	return Fetch(context.Background(), memoryStore, types.NewRequest(imageRef, "", allMediaTypes(), resolver))
}

func allMediaTypes() []string {
	return []string{
		types.MediaTypeDockerSchema2Manifest,
		types.MediaTypeDockerSchema2ManifestList,
		ocispec.MediaTypeImageManifest,
		ocispec.MediaTypeImageIndex,
	}
}
