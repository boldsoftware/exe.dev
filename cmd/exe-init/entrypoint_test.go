package main

import (
	"path/filepath"
	"testing"

	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
)

func TestGetEntrypointAndArgsEntrypointOnly(t *testing.T) {
	ep, err := filepath.EvalSymlinks("/bin/sh")
	assert.NoError(t, err)
	cfg := v1.ImageConfig{
		Entrypoint: []string{
			ep,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, entrypoint, ep)
	assert.Equal(t, args, []string{ep})
}

func TestGetEntrypointAndArgsEntrypointOnlyRelative(t *testing.T) {
	ep := "./app"
	expected := "./app"
	cfg := v1.ImageConfig{
		Entrypoint: []string{
			ep,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, expected, entrypoint)
	assert.Equal(t, []string{expected}, args)
}

func TestGetEntrypointAndArgsEntrypointSlice(t *testing.T) {
	ep, err := filepath.EvalSymlinks("/bin/sh")
	assert.NoError(t, err)
	epAdditional := "bar"
	cfg := v1.ImageConfig{
		Entrypoint: []string{
			ep,
			epAdditional,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, entrypoint, ep)
	assert.Equal(t, args, []string{ep, epAdditional})
}

func TestGetEntrypointAndArgsCmdOnly(t *testing.T) {
	cmd, err := filepath.EvalSymlinks("/bin/sh")
	assert.NoError(t, err)
	cfg := v1.ImageConfig{
		Cmd: []string{
			cmd,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, cmd, entrypoint)
	assert.Equal(t, []string{cmd}, args)
}

func TestGetEntrypointAndArgsCmdSlice(t *testing.T) {
	cmd, err := filepath.EvalSymlinks("/bin/sh")
	assert.NoError(t, err)
	cmdAdditional := "bar"
	cfg := v1.ImageConfig{
		Cmd: []string{
			cmd,
			cmdAdditional,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, entrypoint, cmd)
	assert.Equal(t, args, []string{cmd, cmdAdditional})
}

func TestGetEntrypointAndArgsEntrypointAndCmd(t *testing.T) {
	ep, err := filepath.EvalSymlinks("/bin/sh")
	assert.NoError(t, err)
	cmd := "bar"
	cfg := v1.ImageConfig{
		Entrypoint: []string{
			ep,
			cmd,
		},
	}

	entrypoint, args, err := getEntrypointArgs(cfg)
	assert.NoError(t, err)
	assert.Equal(t, entrypoint, ep)
	assert.Equal(t, args, []string{ep, cmd})
}
