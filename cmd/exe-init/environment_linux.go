//go:build linux

package main

import (
	"os"
)

func configureEnvironment() error {
	// setup env vars
	env := map[string]string{
		"PATH": "/exe.dev/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/",
		"PWD":  "/",
		"TERM": "xterm",
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
