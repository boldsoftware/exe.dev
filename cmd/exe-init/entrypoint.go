package main

import (
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/urfave/cli/v2"
)

var entrypointCommand = &cli.Command{
	Name:   "entrypoint",
	Hidden: true,
	Action: func(clix *cli.Context) error {
		pid, err := runEntrypoint()
		if err != nil {
			return err
		}

		slog.Info("started entrypoint", "pid", pid)

		return nil
	},
}

func getEntrypointArgs(cfg v1.ImageConfig) (string, []string, error) {
	entrypoint := ""
	args := []string{}

	// entrypoint
	switch len(cfg.Entrypoint) {
	case 0:
	case 1:
		entrypoint = cfg.Entrypoint[0]
	default:
		entrypoint = cfg.Entrypoint[0]
		args = cfg.Entrypoint[1:]
	}

	// cmd
	// if entrypoint is empty use the first item of Cmd as the entrypoint
	if entrypoint == "" {
		// cmd
		switch len(cfg.Cmd) {
		case 0:
		case 1:
			entrypoint = cfg.Cmd[0]
		default:
			entrypoint = cfg.Cmd[0]
			args = cfg.Cmd[1:]
		}
	} else { // otherwise use Cmd as args to entrypoint
		args = append(args, cfg.Cmd...)
	}

	// default to /bin/sh
	if entrypoint == "" {
		entrypoint = "/bin/sh"
	}

	// set args for setsid
	cmdArgs := append([]string{entrypoint}, args...)

	// handle relative paths
	if strings.HasPrefix(entrypoint, ".") {
		return entrypoint, append([]string{entrypoint}, args...), nil
	}

	// resolve to full path
	bPath, err := exec.LookPath(entrypoint)
	if err != nil {
		return "", nil, err
	}
	binPath, err := filepath.EvalSymlinks(bPath)
	if err != nil {
		return "", nil, err
	}

	return binPath, cmdArgs, nil
}
