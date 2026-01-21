//go:build arm64

package exelet

import "embed"

//go:embed arm64/*
var archContent embed.FS

const archDir = "arm64"
