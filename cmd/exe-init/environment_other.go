//go:build !linux

package main

func configureEnvironment() error {
	return ErrNotImplemented
}
