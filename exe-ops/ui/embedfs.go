package ui

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var distFS embed.FS

// FS returns the embedded UI filesystem, or nil if dist is empty.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}
