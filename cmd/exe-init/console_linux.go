//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

func setupConsole() error {
	if _, err := os.Stat("/dev/console"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("unable to open /dev/console: %w", err)
	}
	// open /dev/console read/write
	fd, err := unix.Open("/dev/console", unix.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/console: %w", err)
	}
	defer unix.Close(fd)

	// redirect stdin, stdout, stderr to console
	for _, target := range []int{0, 1, 2} {
		if err := unix.Dup2(fd, target); err != nil {
			return fmt.Errorf("dup2 to %d failed: %w", target, err)
		}
	}

	// if fd is not one of 0/1/2, close it
	if fd > 2 {
		unix.Close(fd)
	}

	// start new session
	if _, err := unix.Setsid(); err != nil {
		return fmt.Errorf("setsid failed: %w", err)
	}

	// make stdin the controlling terminal
	if err := unix.IoctlSetInt(0, unix.TIOCSCTTY, 0); err != nil {
		return fmt.Errorf("ioctl set failed: %w", err)
	}

	slog.Debug("controlling terminal setup")

	return nil
}
