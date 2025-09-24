package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

// Dist contains the contents of the built UI under dist/.
//
//go:embed dist/*
var Dist embed.FS

var assets http.FileSystem

func init() {
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		// If the build is misconfigured and dist/ is missing, fail fast.
		panic(err)
	}
	assets = http.FS(sub)
}

// Assets returns an http.FileSystem backed by the embedded UI assets.
func Assets() http.FileSystem {
	return assets
}
