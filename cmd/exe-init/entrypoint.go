package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"exe.dev/exelet/config"
	"github.com/opencontainers/image-spec/specs-go/v1"
)

func getEntrypointArgs(cfg *v1.ImageConfig) (string, []string, error) {
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

func loadImageConfig() (*v1.ImageConfig, error) {
	// load image config
	var imageConfig v1.ImageConfig
	data, err := os.ReadFile(config.ImageConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error reading image config: %w", err)
	}
	if err := json.Unmarshal(data, &imageConfig); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	return &imageConfig, nil
}
