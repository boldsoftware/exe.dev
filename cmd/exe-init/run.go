package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"

	"exe.dev/exelet/config"
	"exe.dev/exelet/utils"
)

func runAction(clix *cli.Context) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)

	// configure system

	// setup environment variables
	// this is to account for very small systems or init that do not
	// setup a basic env (e.g. PATH)
	if err := configureEnvironment(); err != nil {
		return err
	}

	// mount /proc -- this must be first
	if err := mountProc(); err != nil {
		return err
	}

	// mount /dev and /dev/pts
	if err := mountDev(); err != nil {
		return err
	}

	// mount /sys
	if err := mountSysfs(); err != nil {
		return err
	}

	// clean /run
	if err := cleanRun(); err != nil {
		return err
	}

	// network
	if err := configureNetworking(); err != nil {
		return err
	}

	// apply sysctls
	sysctls := map[string]string{
		"net.ipv4.ip_forward": "1",
	}
	for k, v := range sysctls {
		if err := applySysctl(k, v); err != nil {
			return err
		}
	}

	// setup shell
	if err := setupConsole(); err != nil {
		return err
	}

	attr := &syscall.ProcAttr{
		Dir:   "/",
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2}, // stdin, stdout, stderr
		Sys: &syscall.SysProcAttr{
			Setpgid: true, // child gets its own process group
		},
	}

	// resolve shell
	shellPath, err := utils.GetShellPath()
	if err != nil {
		if !errors.Is(err, utils.ErrNotFound) {
			return err
		}
	}

	if shellPath != "" {
		slog.Info("using shell", "path", shellPath)

		pid, err := syscall.ForkExec(shellPath, []string{shellPath}, attr)
		if err != nil {
			return fmt.Errorf("forkexec failed: %w", err)
		}

		slog.Debug("init shell", "pid", pid)
		// give the child’s process group the foreground of the tty
		if err := unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, pid); err != nil {
			return fmt.Errorf("TIOCSPGRP failed: %v", err)
		}
	}

	// start ssh in background
	go func() {
		slog.Info("starting ssh", "path", config.InstanceExeSshPath)
		cmd := exec.Command(config.InstanceExeSshPath,
			"-D",
			"-e",
			"-f",
			"/exe.dev/etc/ssh/sshd_config",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = []string{
			"PATH=/exe.dev/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME=/",
			"PWD=/",
			"TERM=xterm",
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Foreground: false,
			Setsid:     true,
		}
		if err := cmd.Start(); err != nil {
			slog.Error("failed to start ssh process", "err", err)
			return
		}
		if err := cmd.Process.Release(); err != nil {
			slog.Error("failed to release ssh process", "err", err)
			return
		}
	}()

	// entrypoint
	slog.Info("starting entrypoint")
	pid, err := runEntrypoint()
	if err != nil {
		return err
	}
	if pid > -1 {
		slog.Info("started entrypoint", "pid", pid)
	}

	// reap children
	for {
		var status unix.WaitStatus
		_, err := unix.Wait4(-1, &status, 0, nil)
		if err == syscall.ECHILD {
			// no children left, loop
			continue
		}
		if err != nil {
			slog.Error("wait4 error", "err", err)
		}
	}
}
