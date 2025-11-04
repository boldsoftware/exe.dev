package main

import (
	"errors"
	"log/slog"
	"os"

	cli "github.com/urfave/cli/v2"

	"exe.dev/version"
)

const (
	// EnvVarExeCmdlinePath is the environment variable to override the boot args cmdline path
	EnvVarExeCmdlinePath = "EXE_CMDLINE_PATH"
)

// ErrNotImplemented is returned for functionality not yet implemented
var ErrNotImplemented = errors.New("NOT IMPLEMENTED")

func main() {
	app := cli.NewApp()
	app.Name = "exe-init"
	app.Version = version.BuildVersion()
	app.Authors = []*cli.Author{
		{
			Name: "exe.dev",
		},
	}
	app.Usage = version.Name + " (init)"
	app.Action = runAction
	app.Commands = []*cli.Command{
		entrypointCommand,
	}
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
		},
	}
	app.Before = func(clix *cli.Context) error {
		opts := &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}
		if clix.Bool("debug") {
			opts.Level = slog.LevelDebug
		}
		handler := slog.NewTextHandler(os.Stdout, opts)
		log := slog.New(handler)
		slog.SetDefault(log)
		return nil
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error(err.Error())
		panic(err)
	}
}
