//go:build !linux

package main

func setupConsole() error {
	return ErrNotImplemented
}
