package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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

// portHolder returns the PID(s) listening on addr (e.g. ":5555") using lsof.
// Returns "" if it can't determine the holder.
func portHolder(addr string) string {
	port := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		port = addr[i+1:]
	}
	out, err := exec.Command("lsof", "-ti", "tcp:"+port).Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

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
			&cli.StringFlag{
				Name:    "environment",
				Usage:   "environment this exe-ops serves (e.g. prod, staging); displayed in the UI title",
				EnvVars: []string{"EXE_OPS_ENVIRONMENT"},
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
			environment := c.String("environment")
			gitRepo := inventory.NewGitRepo(log, gitRepoDir, c.String("git-repo-url"))
			inv := inventory.New(log, gitRepo)

			// Initialize deploy manager (shares the bare git clone with inventory).
			deployer := deploy.NewManager(ctx, log, gitRepoDir, "deploy-cache")

			// Optionally attach Slack notifications for deploys.
			var slackNotifier *deploy.SlackNotifier
			if sn := deploy.NewSlackNotifier(c.String("slack-bot-token"), log); sn != nil {
				slackNotifier = sn
				deployer.SetNotifier(sn)
				log.Info("slack deploy notifications enabled")
			}

			// Refresh inventory after each deploy so the UI sees updated versions.
			deployer.OnDeploy(inv.Refresh)

			// Opportunistically prebuild exed when main advances, so the
			// artifact is cached by the time someone clicks Deploy.
			gitRepo.OnChange(func(sha string) {
				deployer.Prebuild(ctx, sha, []string{"exed"})
			})

			// Initialize CD scheduler for exed (disabled by default).
			var scheduler *deploy.Scheduler
			if slackNotifier != nil {
				scheduler = deploy.NewScheduler(deployer, gitRepo, slackNotifier, inv, log, environment, "cd-state.json")
			}

			// When TLS is on we're serving over Tailscale, which is also
			// the only way we can identify the peer. Require human-user
			// auth iff TLS is on; local dev (--tls=false) stays open.
			handler := server.New(uiFS, log, environment, inv, deployer, scheduler, useTLS)

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

			// Start git repo, inventory, and CD scheduler services.
			go gitRepo.Run(ctx)
			go inv.Run(ctx)
			if scheduler != nil {
				go scheduler.Run(ctx)
			}

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
					attrs := []any{"error", err}
					var opErr *net.OpError
					if errors.As(err, &opErr) {
						if se := new(os.SyscallError); errors.As(opErr.Err, &se) && se.Err == syscall.EADDRINUSE {
							if pid := portHolder(c.String("addr")); pid != "" {
								attrs = append(attrs, "held_by_pid", pid)
							}
						}
					}
					log.Error("server error", attrs...)
					cancel()
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
