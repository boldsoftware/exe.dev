//go:build linux
// +build linux

package pty

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// TIOCGPTPEER opens the slave end of a PTY directly from the master fd,
// avoiding the race condition inherent in path-based /dev/pts/N opening.
// Available since Linux 4.13.
const TIOCGPTPEER = 0x5441

func open() (pty, tty *os.File, err error) {
	p, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	// In case of error after this point, make sure we close the ptmx fd.
	defer func() {
		if err != nil {
			_ = p.Close() // Best effort.
		}
	}()

	if err := unlockpt(p); err != nil {
		return nil, nil, err
	}

	// Use TIOCGPTPEER to open the slave directly from the master fd.
	// This is race-free: it derives the slave fd from the master
	// without going through a /dev/pts/N path lookup.
	fd, _, errno := syscall.Syscall(syscall.SYS_IOCTL, p.Fd(), TIOCGPTPEER, uintptr(syscall.O_RDWR|syscall.O_NOCTTY))
	if errno == 0 {
		t := os.NewFile(fd, "")
		return p, t, nil
	}

	// Fall back to path-based opening for kernels < 4.13.
	sname, err := ptsname(p)
	if err != nil {
		return nil, nil, err
	}

	t, err := os.OpenFile(sname, os.O_RDWR|syscall.O_NOCTTY, 0) //nolint:gosec // Expected Open from a variable.
	if err != nil {
		return nil, nil, err
	}
	return p, t, nil
}

func ptsname(f *os.File) (string, error) {
	var n _C_uint
	err := ioctl(f, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))) //nolint:gosec // Expected unsafe pointer for Syscall call.
	if err != nil {
		return "", err
	}
	return "/dev/pts/" + strconv.Itoa(int(n)), nil
}

func unlockpt(f *os.File) error {
	var u _C_int
	// use TIOCSPTLCK with a pointer to zero to clear the lock.
	return ioctl(f, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&u))) //nolint:gosec // Expected unsafe pointer for Syscall call.
}
