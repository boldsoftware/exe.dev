package utils

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"exe.dev/deps/image"
	"exe.dev/deps/image/types"
	"exe.dev/exelet/storage"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

const (
	// baseImageSize is the fixed size for base images (10GB sparse)
	baseImageSize = 10 * 1024 * 1024 * 1024
	// tempImagePrefix is the prefix used for temporary image volumes during import
	tempImagePrefix = "tmp-"
)

// LoadImageWithMetadata loads an image into the storageManager using pre-fetched metadata.
// This ensures the digest used for the singleflight key matches the image that gets loaded.
// It creates a 10G sparse volume with a temporary name, fetches the image contents,
// validates with fsck, and then renames to the final name on success.
//
// Note: This function uses a detached context for all operations to ensure that
// context cancellation from any single caller (in a singleflight group) won't
// affect the shared image loading operation. The original context is only used
// for logging.
func LoadImageWithMetadata(ctx context.Context, imageRef, platform string, metadata *types.ImageMetadata, imageManager *image.ImageManager, storageManager storage.StorageManager, log *slog.Logger) (string, error) {
	// use a detached context for all operations to prevent caller cancellation
	// from affecting the shared image load (singleflight). this ensures that
	// concurrent requests waiting on the same image won't be affected if one
	// caller's context is cancelled.
	opCtx := context.Background()

	imageFSID := metadata.Digest
	tempFSID := tempImagePrefix + imageFSID

	// construct a digest-based reference to ensure we fetch the exact image
	// that matches our singleflight key, even if the tag has moved
	digestRef := makeDigestRef(imageRef, metadata.Digest)

	// check if image already exists - skip load if so
	if _, err := storageManager.Get(opCtx, imageFSID); err == nil {
		log.DebugContext(ctx, "image already exists, skipping load", "image", imageRef, "digest", imageFSID)
		return imageFSID, nil
	}

	// check compressed image size early to fail fast if image is too large
	// use a conservative 3x multiplier to estimate uncompressed size
	compressedSize := image.GetManifestSize(metadata.Manifest)
	estimatedSize := compressedSize * 3
	if estimatedSize > baseImageSize {
		return "", fmt.Errorf("image too large: estimated uncompressed size %d bytes (compressed: %d) exceeds maximum %d bytes", estimatedSize, compressedSize, baseImageSize)
	}

	log.DebugContext(ctx, "loading image", "image", imageRef, "digest", imageFSID, "compressedSize", compressedSize, "estimatedSize", estimatedSize)

	// clean up any leftover temp dataset from a previous failed load
	// singleflight coordination ensures we won't have concurrent loads for the same image,
	// so any existing temp dataset is from a crashed previous attempt
	if _, getErr := storageManager.Get(opCtx, tempFSID); getErr == nil {
		log.DebugContext(ctx, "cleaning up leftover temp dataset", "tempFSID", tempFSID)
		_ = storageManager.Unmount(opCtx, tempFSID)
		_ = storageManager.Delete(opCtx, tempFSID)
	}

	// create 10G sparse volume with temporary name
	if _, err := storageManager.Create(opCtx, tempFSID, &storageapi.FilesystemConfig{
		FsType: "ext4",
		Size:   baseImageSize,
	}); err != nil {
		return "", fmt.Errorf("error creating temporary image storage: %w", err)
	}

	// cleanup function for the temporary volume
	cleanup := func() {
		log.DebugContext(ctx, "cleaning up temporary image volume", "tempFSID", tempFSID)
		if unmountErr := storageManager.Unmount(opCtx, tempFSID); unmountErr != nil {
			log.WarnContext(ctx, "error unmounting temporary image volume during cleanup", "error", unmountErr)
		}
		if deleteErr := storageManager.Delete(opCtx, tempFSID); deleteErr != nil {
			log.WarnContext(ctx, "error deleting temporary image volume during cleanup", "error", deleteErr)
		}
	}

	// mount temporary volume
	log.DebugContext(ctx, "mounting temporary image volume", "tempFSID", tempFSID)
	mountConfig, err := storageManager.Mount(opCtx, tempFSID)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("error mounting temporary image storage: %w", err)
	}

	// fetch image contents using digest-based reference
	log.DebugContext(ctx, "fetching image contents", "image", imageRef, "digestRef", digestRef)
	_, fetchErr := imageManager.Fetch(opCtx, digestRef, platform, mountConfig.Path)

	// unmount before checking fetch error
	log.DebugContext(ctx, "unmounting temporary image volume", "tempFSID", tempFSID)
	if err = storageManager.Unmount(opCtx, tempFSID); err != nil {
		// if unmount fails, still try to cleanup
		cleanup()
		return "", fmt.Errorf("error unmounting temporary image storage: %w", err)
	}

	// check fetch error after unmount
	if fetchErr != nil {
		cleanup()
		return "", fmt.Errorf("error fetching image contents: %w", fetchErr)
	}

	// run fsck to validate filesystem integrity
	log.DebugContext(ctx, "validating image filesystem", "tempFSID", tempFSID)
	if err := storageManager.Fsck(opCtx, tempFSID); err != nil {
		cleanup()
		return "", fmt.Errorf("image filesystem validation failed: %w", err)
	}

	// rename to final name
	log.DebugContext(ctx, "renaming image volume to final name", "tempFSID", tempFSID, "imageFSID", imageFSID)
	if err := storageManager.Rename(opCtx, tempFSID, imageFSID); err != nil {
		cleanup()
		return "", fmt.Errorf("error renaming image volume: %w", err)
	}

	log.DebugContext(ctx, "image loaded successfully", "image", imageRef, "imageFSID", imageFSID)
	return imageFSID, nil
}

// makeDigestRef converts an image reference (which may be tag-based) to a digest-based reference.
// For example: "docker.io/library/ubuntu:latest" + "sha256:abc..." -> "docker.io/library/ubuntu@sha256:abc..."
func makeDigestRef(imageRef, digest string) string {
	// Remove any existing tag or digest from the reference
	ref := imageRef
	if idx := strings.LastIndex(ref, "@"); idx != -1 {
		ref = ref[:idx]
	} else if idx := strings.LastIndex(ref, ":"); idx != -1 {
		// Check if this colon is part of a port (e.g., localhost:5000/image)
		// by seeing if there's a slash after it
		afterColon := ref[idx+1:]
		if !strings.Contains(afterColon, "/") {
			ref = ref[:idx]
		}
	}
	return ref + "@" + digest
}
