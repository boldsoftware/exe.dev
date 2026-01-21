//go:build amd64

package exelet

import "embed"

//go:embed amd64/*
var archContent embed.FS

const archDir = "amd64"
