package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/exelet/config"
)

func startSSH(imageConfig *v1.ImageConfig) error {
	// check for privilege separation user "sshd" for ssh daemon
	if _, err := user.Lookup(config.InstanceExeSshPrivilegeSeparationUser); err != nil {
		// if not UnknownUserError, return err
		if _, ok := err.(user.UnknownUserError); !ok {
			return err
		}
		// attempt to create
		if err := addSSHUser(config.InstanceExeSshPrivilegeSeparationUser); err != nil {
			return err
		}
	}

	// check to set ownership on ssh
	if v, ok := imageConfig.Labels[config.InstanceExeLabelLoginUser]; ok {
		slog.Info("setting ssh public key permissions for exe.dev", "user", v)
		// lookup user to resolve uid and gid
		u, err := user.Lookup(v)
		// if there is an error, don't return but instead keep the ownership as root:root
		// to ensure the user can still login via ssh as root to debug, etc.
		if err != nil {
			slog.Error("error looking up exe user", "username", v, "err", err)
		}
		if u != nil {
			uid, err := strconv.Atoi(u.Uid)
			if err != nil {
				slog.Error("error parsing user id", "user", v, "err", err)
			}
			gid, err := strconv.Atoi(u.Gid)
			if err != nil {
				slog.Error("error parsing group id", "user", v, "err", err)
			}
			if err := os.Chown(config.InstanceSSHPublicKeysPath, uid, gid); err != nil {
				slog.Error("error setting ssh permissions", "user", v, "err", err)
			}
		}
	}

	// start ssh in background
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
		return fmt.Errorf("failed to start ssh process: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		slog.Error("failed to release ssh process", "err", err)
		return fmt.Errorf("failed to release ssh process: %w", err)
	}

	return nil
}

func addSSHUser(username string) error {
	st, err := os.Stat(config.PasswdPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.PasswdPath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(config.PasswdPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, st.Mode())
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s:x:22:22:nobody:/dev/null:/sbin/nologin\n", username); err != nil {
		return err
	}
	return nil
}
