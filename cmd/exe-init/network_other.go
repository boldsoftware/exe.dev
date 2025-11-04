//go:build !linux

package main

func configureNetworking() error {
	return ErrNotImplemented
}
