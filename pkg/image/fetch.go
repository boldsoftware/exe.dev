package image

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/containerd/containerd/v2/pkg/archive/compression"
	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/pkg/image/registry"
	"exe.dev/pkg/image/store"
	"exe.dev/pkg/image/types"
	"exe.dev/pkg/image/util"
)

func (i *ImageManager) Fetch(ctx context.Context, ref, platform, destDir string) (*ocispec.Image, error) {
	imageRef, err := util.ParseName(ref)
	if err != nil {
		return nil, err
	}
	if _, ok := imageRef.(reference.NamedTagged); !ok {
		if _, ok := imageRef.(reference.Digested); !ok {
			return nil, fmt.Errorf("image reference must include a tag or a digest")
		}
	}

	contentStore, err := store.NewContentStore(i.config.DataDir)
	if err != nil {
		return nil, err
	}

	err = util.CreateRegistryHost(imageRef, i.config.Username, i.config.Password, i.config.Insecure,
		i.config.UseHTTP, "", false)
	if err != nil {
		return nil, fmt.Errorf("error creating registry host configuration: %v", err)
	}

	descriptor, err := registry.FetchDescriptor(util.GetResolver(), contentStore, imageRef)
	if err != nil {
		return nil, err
	}

	_, db, _ := contentStore.Get(descriptor)
	switch descriptor.MediaType {
	case ocispec.MediaTypeImageIndex, types.MediaTypeDockerSchema2ManifestList:
		// this is a multi-platform image descriptor; marshal to Index type
		var idx ocispec.Index
		if err := json.Unmarshal(db, &idx); err != nil {
			return nil, err
		}

		return i.fetchIndex(ctx, contentStore, idx, platform, destDir)
	case ocispec.MediaTypeImageManifest, types.MediaTypeDockerSchema2Manifest:
		var man ocispec.Manifest
		if err := json.Unmarshal(db, &man); err != nil {
			return nil, err
		}
		_, cb, _ := contentStore.Get(man.Config)
		var conf ocispec.Image
		if err := json.Unmarshal(cb, &conf); err != nil {
			return nil, err
		}
		i.log.Debug("image config", "config", conf)
	default:
		i.log.Error("unknown descriptor", "type", descriptor.MediaType)
	}

	return nil, nil
}

func (i *ImageManager) fetchIndex(ctx context.Context, cs *store.ContentStore, idx ocispec.Index, platform, destDir string) (*ocispec.Image, error) {
	i.log.Debug("fetching content for index", "subject", idx.Subject)
	var conf ocispec.Image
	for _, img := range idx.Manifests {
		// check platform
		if strings.EqualFold(platform, fmt.Sprintf("%s/%s", img.Platform.OS, img.Platform.Architecture)) {
			i.log.Debug("fetching manifest", "digest", img.Digest, "os", img.Platform.OS, "arch", img.Platform.Architecture)
			_, db, _ := cs.Get(img)
			switch img.MediaType {
			case ocispec.MediaTypeImageManifest, types.MediaTypeDockerSchema2Manifest:
				var man ocispec.Manifest
				if err := json.Unmarshal(db, &man); err != nil {
					return nil, err
				}
				for _, layer := range man.Layers {
					i.log.Debug("fetching layer", "type", layer.MediaType, "size", layer.Size)
					ra, err := cs.ReaderAt(ctx, layer)
					if err != nil {
						return nil, err
					}
					cr := content.NewReader(ra)
					r, err := compression.DecompressStream(cr)
					if err != nil {
						ra.Close()
						return nil, err
					}
					opts := []archive.ApplyOpt{
						archive.WithNoSameOwner(),
					}
					if _, err := archive.Apply(ctx, destDir, r, opts...); err != nil {
						r.Close()
						ra.Close()
						return nil, err
					}
					r.Close()
					ra.Close()
				}
				// image config
				_, cb, _ := cs.Get(man.Config)
				if err := json.Unmarshal(cb, &conf); err != nil {
					return nil, err
				}
				i.log.Debug("image configuration", "config", conf)
			default:
				return nil, fmt.Errorf("unknown media type: %+v", img.MediaType)
			}
		}
	}
	return &conf, nil
}

func (i *ImageManager) fetchManifest(ctx context.Context, manifest ocispec.Manifest, destDir string) error {
	return fmt.Errorf("fetching manifest is not implemented")
}
