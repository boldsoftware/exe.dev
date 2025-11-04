//go:build !linux

package main

func runEntrypoint() (int, error) {
	return -1, ErrNotImplemented
}
