//go:build !linux

package main

import v1 "github.com/opencontainers/image-spec/specs-go/v1"

func runEntrypoint(_ *v1.ImageConfig) (int, error) {
	return -1, ErrNotImplemented
}
