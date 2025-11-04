package main

import (
	"log/slog"
	"os"

	cli "github.com/urfave/cli/v2"

	"exe.dev/exelet/config"
	"exe.dev/version"
)

func main() {
	app := cli.NewApp()
	app.Name = "exe-ssh"
	app.Version = version.BuildVersion()
	app.Authors = []*cli.Author{
		{
			Name: "exe.dev",
		},
	}
	app.Usage = version.Name + " (ssh)"
	app.Action = runAction
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
		},
		&cli.StringFlag{
			Name:    "listen-addr",
			Aliases: []string{"l"},
			Usage:   "listen address",
			Value:   ":22",
		},
		&cli.StringFlag{
			Name:  "host-key",
			Usage: "path to host identity key",
			Value: config.InstanceSSHHostKeyPath,
		},
		&cli.StringFlag{
			Name:  "authorized-keys",
			Usage: "path to authorized public keys",
			Value: config.InstanceSSHPublicKeysPath,
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
