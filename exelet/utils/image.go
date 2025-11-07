package utils

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"exe.dev/deps/image"
	"exe.dev/exelet/storage"
	storageapi "exe.dev/pkg/api/exe/storage/v1"
)

// LoadImage is a helper that will load the specific imageRef into the storageManager.
func LoadImage(ctx context.Context, imageRef string, platform string, imageManager *image.ImageManager, storageManager storage.StorageManager, log *slog.Logger) (string, error) {
	imageMetadata, err := imageManager.FetchManifestForPlatform(ctx, imageRef, platform)
	if err != nil {
		return "", fmt.Errorf("error fetching image manifest: %w", err)
	}
	imageFSID := imageMetadata.Digest

	// setup image - this uses the image metadata to create an image fs that is the size of the image contents
	imageContentSize, err := imageManager.GetImageSize(ctx, imageRef, platform)
	if err != nil {
		return "", fmt.Errorf("error getting image size: %w", err)
	}

	log.Debug("image content", "compressedSize", imageContentSize)

	// Check for negative size
	if imageContentSize < 0 {
		return "", fmt.Errorf("invalid image size: %d", imageContentSize)
	}

	// use a large multiplier to ensure we have plenty of volume space
	// for the uncompressed image. as get the result after the unpack
	// and adjust the final image volume size to be accurate.
	const sizeMultiplier = 10
	maxSafe := uint64(math.MaxUint64 / sizeMultiplier)
	if uint64(imageContentSize) > maxSafe {
		return "", fmt.Errorf("image too large: would exceed filesystem limits (%d bytes)", imageContentSize)
	}

	imageSize := uint64(float64(imageContentSize) * sizeMultiplier)
	if _, err := storageManager.Create(ctx, imageFSID, &storageapi.FilesystemConfig{
		FsType: "ext4", // TODO: support different formats?
		Size:   imageSize,
	}); err != nil {
		return "", fmt.Errorf("error creating instance storage: %w", err)
	}

	// fetch / unpack image content to snapshot
	log.Debug("fetching and unpacking image", "image", imageRef)

	// fetch image contents
	mountConfig, err := storageManager.Mount(ctx, imageFSID)
	if err != nil {
		return "", fmt.Errorf("error mounting image fs storage: %w", err)
	}

	log.Debug("fetching image contents", "image", imageRef)
	if _, err := imageManager.Fetch(ctx, imageRef, platform, mountConfig.Path); err != nil {
		return "", err
	}

	// unmount image storage
	if err = storageManager.Unmount(ctx, imageFSID); err != nil {
		return "", fmt.Errorf("error unmounting image fs for %s: %w", imageFSID, err)
	}

	// resize the final image volume if the result was
	log.Debug("shrinking image fs", "image", imageRef)
	if err := storageManager.Shrink(ctx, imageFSID); err != nil {
		return "", fmt.Errorf("error resizing image fs for %s: %w", imageFSID, err)
	}

	return imageFSID, nil
}
