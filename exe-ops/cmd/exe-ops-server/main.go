package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"exe.dev/exe-ops/server"
	"exe.dev/exe-ops/server/deploy"
	"exe.dev/exe-ops/server/inventory"
	"exe.dev/exe-ops/ui"
	"exe.dev/exe-ops/version"
	"github.com/tailscale/tscert"
	"github.com/urfave/cli/v2"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	app := &cli.App{
		Name:    "exe-ops-server",
		Usage:   "Deploy management server for exe-ops",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Usage:   "listen address",
				Value:   ":5555",
				EnvVars: []string{"EXE_OPS_ADDR"},
			},
			&cli.BoolFlag{
				Name:    "tls",
				Usage:   "enable TLS via Tailscale (requires tailscaled)",
				Value:   true,
				EnvVars: []string{"EXE_OPS_TLS"},
			},
			&cli.StringFlag{
				Name:    "git-repo-dir",
				Usage:   "path for bare git clone of exe repo",
				Value:   "exe-repo.git",
				EnvVars: []string{"EXE_OPS_GIT_REPO_DIR"},
			},
			&cli.StringFlag{
				Name:    "git-repo-url",
				Usage:   "git URL of exe repo",
				Value:   "git@github.com:boldsoftware/exe.git",
				EnvVars: []string{"EXE_OPS_GIT_REPO_URL"},
			},
			&cli.StringFlag{
				Name:    "slack-bot-token",
				Usage:   "Slack bot token for deploy notifications (posts to #ship/#boat)",
				EnvVars: []string{"EXE_SLACK_BOT_TOKEN"},
			},
		},
		Action: func(c *cli.Context) error {
			useTLS := c.Bool("tls")

			if useTLS {
				const sock = "/var/run/tailscale/tailscaled.sock"
				if _, err := os.Stat(sock); err != nil {
					return fmt.Errorf("tailscaled socket not found at %s (is tailscaled running?): %w", sock, err)
				}
			}

			uiFS := ui.FS()

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// Initialize git repo and inventory services.
			gitRepoDir := c.String("git-repo-dir")
			gitRepo := inventory.NewGitRepo(log, gitRepoDir, c.String("git-repo-url"))
			inv := inventory.New(log, gitRepo)

			// Initialize deploy manager (shares the bare git clone with inventory).
			deployer := deploy.NewManager(ctx, log, gitRepoDir, "deploy-cache")

			// Optionally attach Slack notifications for deploys.
			if sn := deploy.NewSlackNotifier(c.String("slack-bot-token"), log); sn != nil {
				deployer.SetNotifier(sn)
				log.Info("slack deploy notifications enabled")
			}

			// Refresh inventory after each deploy so the UI sees updated versions.
			deployer.OnDeploy(inv.Refresh)

			handler := server.New(uiFS, log, inv, deployer)

			srv := &http.Server{
				Addr:        c.String("addr"),
				Handler:     handler,
				ReadTimeout: 15 * time.Second,
				IdleTimeout: 60 * time.Second,
				// No WriteTimeout: SSE connections are long-lived.
				// Individual request timeouts are handled at the handler level.
				BaseContext: func(_ net.Listener) context.Context { return ctx },
			}

			if useTLS {
				srv.TLSConfig = &tls.Config{
					GetCertificate: tscert.GetCertificate,
					MinVersion:     tls.VersionTLS12,
				}
			}

			// Start git repo and inventory services.
			go gitRepo.Run(ctx)
			go inv.Run(ctx)

			// Start server in goroutine.
			go func() {
				log.Info("server starting", "addr", c.String("addr"), "tls", useTLS, "version", version.Full())
				var err error
				if useTLS {
					err = srv.ListenAndServeTLS("", "")
				} else {
					err = srv.ListenAndServe()
				}
				if err != nil && err != http.ErrServerClosed {
					log.Error("server error", "error", err)
				}
			}()

			<-ctx.Done()
			log.Info("shutting down")

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			return srv.Shutdown(shutdownCtx)
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}
