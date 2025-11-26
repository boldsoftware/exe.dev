package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/crypto/ssh"

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

	// ensure ssh host key exists (generate if missing)
	if err := ensureSSHHostKey(); err != nil {
		return fmt.Errorf("failed to ensure ssh host key: %w", err)
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

func ensureSSHHostKey() error {
	// Check if host key already exists
	if _, err := os.Stat(config.InstanceSSHHostKeyPath); err == nil {
		slog.Info("using existing ssh host key", "path", config.InstanceSSHHostKeyPath)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to check ssh host key: %w", err)
	}

	// Host key doesn't exist, generate new Ed25519 key pair
	slog.Warn("ssh host key not found, auto-generating new key pair", "path", config.InstanceSSHHostKeyPath)

	// Create directory if it doesn't exist
	keyDir := filepath.Dir(config.InstanceSSHHostKeyPath)
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return fmt.Errorf("failed to create ssh key directory: %w", err)
	}

	// Generate Ed25519 key pair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate ed25519 key: %w", err)
	}

	// Marshal private key in OpenSSH format
	privKeyBlock, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	// Encode PEM block to bytes
	privKeyPEM := pem.EncodeToMemory(privKeyBlock)

	// Write private key
	if err := os.WriteFile(config.InstanceSSHHostKeyPath, privKeyPEM, 0o600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	// Marshal public key in OpenSSH format
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("failed to create ssh public key: %w", err)
	}
	pubKeyBytes := ssh.MarshalAuthorizedKey(sshPubKey)

	// Write public key
	pubKeyPath := config.InstanceSSHHostKeyPath + ".pub"
	if err := os.WriteFile(pubKeyPath, pubKeyBytes, 0o644); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	slog.Info("ssh host key pair generated successfully", "private", config.InstanceSSHHostKeyPath, "public", pubKeyPath)
	return nil
}

func addSSHUser(username string) error {
	st, err := os.Stat(config.PasswdPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.PasswdPath), 0o755); err != nil {
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
