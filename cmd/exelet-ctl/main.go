package main

import (
	"log/slog"
	"os"

	cli "github.com/urfave/cli/v2"

	"exe.dev/cmd/exelet-ctl/compute"
	"exe.dev/exelet/config"
	"exe.dev/version"
)

func main() {
	app := cli.NewApp()
	app.Name = version.Name
	app.Version = version.BuildVersion()
	// disable flag separator to be able to have custom "key=val,key=value" options
	app.DisableSliceFlagSeparator = true
	app.Authors = []*cli.Author{
		{
			Name: "exe.dev",
		},
	}
	app.Usage = version.Description
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
		},
		&cli.StringFlag{
			Name:    "addr",
			Aliases: []string{"a"},
			Usage:   "exelet server address",
			Value:   config.DefaultExeletAddress,
			EnvVars: []string{config.EnvVarExeletServerAddress},
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
	app.Commands = []*cli.Command{
		compute.Command,
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error(err.Error())
		panic(err)
	}
}
