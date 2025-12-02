//go:build linux

package main

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"exe.dev/exelet/config"
)

func runEntrypoint(imageConfig *v1.ImageConfig) (int, error) {
	// entrypoint and args
	entrypoint, args, err := getEntrypointArgs(imageConfig)
	if err != nil {
		return -1, err
	}
	env := os.Environ()
	cwd := "/"

	// env
	if len(imageConfig.Env) > 0 {
		env = append(env, imageConfig.Env...)
	}

	// load env from config
	if _, err := os.Stat(config.EnvConfigPath); err == nil {
		slog.Info("loading environment", "path", config.EnvConfigPath)
		f, err := os.Open(config.EnvConfigPath)
		if err != nil {
			return -1, err
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if line != "" {
				env = append(env, line)
			}
		}
	}

	if v := imageConfig.WorkingDir; v != "" {
		cwd = v
	}

	// check if wrapping another init
	if isInitSystem(entrypoint) {
		slog.Info("handing off init", "init", entrypoint)
		// exec and hand off PID 1
		if err := syscall.Exec(entrypoint, args, env); err != nil {
			return -1, err
		}
		return 0, nil
	}

	slog.Info("running entrypoint",
		"cwd", cwd,
		"entrypoint", entrypoint,
		"args", args,
	)

	// entrypoint exec
	cmd := exec.Command(entrypoint)
	cmd.Args = args
	cmd.Env = env
	cmd.Dir = cwd

	// TODO: move std i/o to vsock to control from exelet ?
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Foreground: false,
		Setsid:     true,
	}
	slog.Debug("starting process", "entrypoint", entrypoint)
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	pid := cmd.Process.Pid
	slog.Debug("process running", "pid", cmd.Process.Pid)

	slog.Debug("releasing process", "entrypoint", entrypoint)
	if err := cmd.Process.Release(); err != nil {
		return -1, err
	}

	return pid, nil
}

func isInitSystem(v string) bool {
	return strings.Contains(v, "/init") ||
		strings.Contains(v, "systemd") ||
		strings.Contains(v, "runit") ||
		strings.Contains(v, "s6-overlay")
}
