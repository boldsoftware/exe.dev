package utils

import (
	"context"
	"fmt"
	"log/slog"

	"exe.dev/deps/image"
	"exe.dev/exelet/storage"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

const (
	// baseImageSize is the fixed size for base images (10GB sparse)
	baseImageSize = 10 * 1024 * 1024 * 1024
	// tempImagePrefix is the prefix used for temporary image volumes during import
	tempImagePrefix = "tmp-"
)

// LoadImage is a helper that will load the specific imageRef into the storageManager.
// It creates a 10G sparse volume with a temporary name, fetches the image contents,
// validates with fsck, and then renames to the final name on success.
//
// Note: This function uses a detached context for all operations to ensure that
// context cancellation from any single caller (in a singleflight group) won't
// affect the shared image loading operation. The original context is only used
// for logging.
func LoadImage(ctx context.Context, imageRef, platform string, imageManager *image.ImageManager, storageManager storage.StorageManager, log *slog.Logger) (string, error) {
	// use a detached context for all operations to prevent caller cancellation
	// from affecting the shared image load (singleflight). this ensures that
	// concurrent requests waiting on the same image won't be affected if one
	// caller's context is cancelled.
	opCtx := context.Background()

	imageMetadata, err := imageManager.FetchManifestForPlatform(opCtx, imageRef, platform)
	if err != nil {
		return "", fmt.Errorf("error fetching image manifest: %w", err)
	}
	imageFSID := imageMetadata.Digest
	tempFSID := tempImagePrefix + imageFSID

	// check if image already exists - skip load if so
	if _, err := storageManager.Get(opCtx, imageFSID); err == nil {
		log.DebugContext(ctx, "image already exists, skipping load", "image", imageRef, "digest", imageFSID)
		return imageFSID, nil
	}

	// check compressed image size early to fail fast if image is too large
	// use a conservative 3x multiplier to estimate uncompressed size
	compressedSize := image.GetManifestSize(imageMetadata.Manifest)
	estimatedSize := compressedSize * 3
	if estimatedSize > baseImageSize {
		return "", fmt.Errorf("image too large: estimated uncompressed size %d bytes (compressed: %d) exceeds maximum %d bytes", estimatedSize, compressedSize, baseImageSize)
	}

	log.DebugContext(ctx, "loading image", "image", imageRef, "digest", imageFSID, "compressedSize", compressedSize, "estimatedSize", estimatedSize)

	// clean up any leftover temp dataset from a previous failed load
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

	// fetch image contents
	log.DebugContext(ctx, "fetching image contents", "image", imageRef)
	_, fetchErr := imageManager.Fetch(opCtx, imageRef, platform, mountConfig.Path)

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
