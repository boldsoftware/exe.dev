//go:build !linux

package main

import "github.com/urfave/cli/v2"

func startMemd() {}

func runMemdAction(_ *cli.Context) error { return nil }
