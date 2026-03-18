package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"exe.dev/exe-ops/agent"
	"exe.dev/exe-ops/agent/client"
	"exe.dev/exe-ops/version"
	"github.com/urfave/cli/v2"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	app := &cli.App{
		Name:    "exe-ops-agent",
		Usage:   "Lightweight system metrics agent for exe-ops",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "server",
				Usage:    "exe-ops server URL",
				Required: true,
				EnvVars:  []string{"EXE_OPS_SERVER"},
			},
			&cli.StringFlag{
				Name:     "token",
				Usage:    "authentication token",
				Required: true,
				EnvVars:  []string{"EXE_OPS_TOKEN"},
			},
			&cli.StringFlag{
				Name:    "name",
				Usage:   "server name (default: hostname)",
				EnvVars: []string{"EXE_OPS_NAME"},
			},
			&cli.DurationFlag{
				Name:  "interval",
				Usage: "collection interval",
				Value: 30_000_000_000, // 30s
			},
			&cli.StringFlag{
				Name:  "tags",
				Usage: "comma-separated tags",
			},
		},
		Action: func(c *cli.Context) error {
			name := c.String("name")
			if name == "" {
				h, err := os.Hostname()
				if err != nil {
					return err
				}
				name = h
			}

			var tags []string
			if t := c.String("tags"); t != "" {
				tags = strings.Split(t, ",")
			}

			fmt.Fprintf(os.Stderr, "exe-ops-agent version %s\n", version.Version)

			cl := client.New(c.String("server"), c.String("token"))
			a := agent.New(name, tags, version.Version, c.Duration("interval"), cl, log)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			return a.Run(ctx)
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}
