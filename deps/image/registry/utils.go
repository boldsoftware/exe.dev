package registry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/deps/image/store"
	"exe.dev/deps/image/types"
)

func skippable(mediaType string) bool {
	// skip foreign/non-distributable layers
	if strings.Index(mediaType, "foreign") > 0 || strings.Index(mediaType, "nondistributable") > 0 {
		return true
	}
	// skip manifests (OCI or Dockerv2) as they are already handled on push references code
	switch mediaType {
	case ocispec.MediaTypeImageManifest, types.MediaTypeDockerSchema2Manifest:
		return true
	}
	return false
}

func isAttestationManifest(desc ocispec.Descriptor) bool {
	if aRefType, ok := desc.Annotations["vnd.docker.reference.type"]; ok {
		if aRefType == "attestation-manifest" {
			return true
		}
	}
	return false
}

func getImagesFromIndex(desc ocispec.Descriptor, ms *store.ContentStore) ([]ocispec.Descriptor, []ocispec.Descriptor) {
	var (
		manifests    []ocispec.Descriptor
		attestations []ocispec.Descriptor
	)
	_, db, _ := ms.Get(desc)
	var index ocispec.Index
	if err := json.Unmarshal(db, &index); err != nil {
		slog.Error("could not unmarshal index from descriptor", "digest", desc.Digest.String(), "err", err)
		return manifests, attestations
	}
	for _, man := range index.Manifests {
		if isAttestationManifest(man) {
			attestations = append(attestations, man)
		} else {
			manifests = append(manifests, man)
		}
	}
	return manifests, attestations
}

func getPlatformString(platform *ocispec.Platform) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		platform.Architecture,
		platform.OS,
		platform.Variant,
		platform.OSVersion,
		strings.Join(platform.OSFeatures, "."))
}
