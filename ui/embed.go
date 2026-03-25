// Package ui embeds the built Vue SPA assets.
//
// Before building cmd/exed, run:
//
//	cd ui && pnpm install --frozen-lockfile && pnpm build
//
// This populates ui/dist/ which is then embedded into the binary.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded dist/ filesystem (rooted at dist/).
// Returns nil if the dist directory is empty or only contains .gitkeep.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// HasIndex reports whether the embedded dist contains index.html,
// i.e. whether the UI was built before compiling the binary.
func HasIndex() bool {
	_, err := fs.Stat(distFS, "dist/index.html")
	return err == nil
}
