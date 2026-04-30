//go:build !linux

package main

import "github.com/urfave/cli/v2"

func startOperatorSSH() {}

func spawnDetachedSubcommand(_, _ string) {}

func runOperatorSSHAction(_ *cli.Context) error { return nil }
