//go:build linux

package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func mountProc() error {
	if err := os.MkdirAll("/proc", 0o755); err != nil {
		return fmt.Errorf("error creating /proc mountpoint: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", uintptr(0), ""); err != nil {
		// already mounted
		if err == unix.EBUSY {
			return nil
		}
		return fmt.Errorf("error mounting /proc: %w", err)
	}

	return nil
}

func mountDev() error {
	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		return err
	}
	if err := unix.Mount("devpts", "/dev/pts", "devpts", uintptr(0), ""); err != nil {
		// already mounted
		if err == unix.EBUSY {
			return nil
		}
		return fmt.Errorf("error mounting /dev/pts: %w", err)
	}

	if err := os.MkdirAll("/dev/shm", 0o1777); err != nil {
		return err
	}
	if err := unix.Mount("shm", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		if err == unix.EBUSY {
			return nil
		}
		return fmt.Errorf("error mounting /dev/shm: %w", err)
	}

	// check for special devices -- this should only happen in "scratch" like environments

	// check for /dev/console
	if _, err := os.Stat("/dev/console"); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("error checking /dev/console: %w", err)
		}
		// attempt to create
		if err := syscall.Mknod("/dev/console", uint32(syscall.S_IFCHR|0o600), devID(5, 1)); err != nil {
			return fmt.Errorf("error creating /dev/console: %w", err)
		}
	}

	// check /dev/null
	if _, err := os.Stat("/dev/null"); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("error checking /dev/null: %w", err)
		}
		// attempt to create
		if err := syscall.Mknod("/dev/null", uint32(syscall.S_IFCHR|0o666), devID(1, 3)); err != nil {
			return fmt.Errorf("error creating /dev/null: %w", err)
		}
	}

	return nil
}

func mountSysfs() error {
	if err := os.MkdirAll("/sys", 0o755); err != nil {
		return fmt.Errorf("error creating /sys mountpoint: %w", err)
	}
	if err := unix.Mount("none", "/sys", "sysfs", uintptr(0), ""); err != nil {
		// already mounted
		if err == unix.EBUSY {
			return nil
		}
		return fmt.Errorf("error mounting /sys: %w", err)
	}
	// cgroup2
	if err := unix.Mount("cgroup2", "/sys/fs/cgroup", "cgroup2", uintptr(0), ""); err != nil {
		// already mounted
		if err == unix.EBUSY {
			return nil
		}
		return fmt.Errorf("error mounting cgroup2: %w", err)
	}

	return nil
}

func cleanRun() error {
	_ = os.RemoveAll("/run")
	if err := os.MkdirAll("/run", 0o755); err != nil {
		return err
	}

	return nil
}

func getBootArg(name string) (string, error) {
	cmdlineFile := "/proc/cmdline"
	// enable configuring cmdline path from env for debug
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.EqualFold(parts[0], EnvVarExeCmdlinePath) {
			cmdlineFile = parts[1]
		}
	}

	data, err := os.ReadFile(cmdlineFile)
	if err != nil {
		return "", err
	}

	args := strings.FieldsSeq(string(data))
	for arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		key := ""
		val := ""
		switch len(parts) {
		case 0:
			continue
		case 1:
			key = arg
		case 2:
			key = parts[0]
			val = parts[1]
		default:
			return "", fmt.Errorf("unexpected boot arg format for %s", arg)
		}

		if strings.EqualFold(key, name) {
			return val, nil
		}
	}

	return "", nil
}

func applySysctl(key, val string) error {
	p := path.Join("/proc/sys", strings.ReplaceAll(key, ".", "/"))
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("error opening sysctl %s: %w", p, err)
	}
	defer f.Close()

	if _, err := f.WriteString(val); err != nil {
		return fmt.Errorf("error writing sysctl %s: %w", p, err)
	}

	return nil
}

func devID(major, minor uint32) int {
	return int(((major & 0xfff) << 8) | (minor & 0xff) | ((minor & ^uint32(0xff)) << 12))
}
