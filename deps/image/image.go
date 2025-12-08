package image

import (
	"log/slog"
	"os"
)

type ImageManager struct {
	config        *Config
	log           *slog.Logger
	metadataCache *MetadataCache
}

// NewImageManager returns a new OCI image manager
func NewImageManager(cfg *Config, log *slog.Logger) (*ImageManager, error) {
	if v := cfg.DataDir; v == "" {
		tmpDir, err := os.MkdirTemp("", "exe-content-")
		if err != nil {
			return nil, err
		}
		cfg.DataDir = tmpDir
	}
	return &ImageManager{
		config:        cfg,
		log:           log,
		metadataCache: NewMetadataCache(cfg.DataDir),
	}, nil
}
