package raw

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// LoopControl device constants
const (
	LOOP_CTL_GET_FREE = 0x4C82 // ioctl for /dev/loop-control
	LOOP_SET_FD       = 0x4C00
	LOOP_CLR_FD       = 0x4C01
)

func formatDisk(diskPath, fsType string) error {
	binPath, err := exec.LookPath(fmt.Sprintf("mkfs.%s", fsType))
	if err != nil {
		return fmt.Errorf("mkfs.%s not found in PATH: %w", fsType, err)
	}

	args := []string{
		"-F",
		diskPath,
	}

	cmd := exec.Command(binPath, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error formatting %s: %w", diskPath, err)
	}

	return nil
}

func setupLoopDevice(diskPath string) (string, error) {
	ctl, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("error opening /dev/loop-control: %w", err)
	}
	defer ctl.Close()

	loopNum, _, errno := unix.Syscall(unix.SYS_IOCTL, ctl.Fd(), LOOP_CTL_GET_FREE, 0)
	if errno != 0 {
		return "", fmt.Errorf("error getting free loop device: %w", errno)
	}

	loopPath := fmt.Sprintf("/dev/loop%d", loopNum)

	loop, err := os.OpenFile(loopPath, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("error opening loop device %s: %w", loopPath, err)
	}
	defer loop.Close()

	file, err := os.OpenFile(diskPath, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("error opening disk image %s: %w", diskPath, err)
	}
	defer file.Close()

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, loop.Fd(), LOOP_SET_FD, file.Fd()); errno != 0 {
		return "", fmt.Errorf("error setting loop device: %w", errno)
	}

	return loopPath, nil
}

func detachLoopDevice(loopPath string) error {
	loop, err := os.OpenFile(loopPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("error opening loop %s: %w", loopPath, err)
	}
	defer loop.Close()

	if err := unix.IoctlSetInt(int(loop.Fd()), LOOP_CLR_FD, 0); err != nil {
		// no such device so return nil
		if err == syscall.ENXIO {
			return nil
		}
		return fmt.Errorf("error detaching loop %s: %w", loopPath, err)
	}

	return nil
}
