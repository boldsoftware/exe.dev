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

// openListeners resolves addr to a set of bind addresses and opens a
// TCP listener for each, wrapping in TLS when tlsConfig is non-nil.
//
// The "tailscale:<port>" sentinel binds to this host's tailnet IPs
// (typically one IPv4 and one IPv6) only, so the public network never
// sees the port. Any other addr is bound directly.
func openListeners(ctx context.Context, addr string, tlsConfig *tls.Config) ([]net.Listener, error) {
	var addrs []string
	if port, ok := strings.CutPrefix(addr, "tailscale:"); ok {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		ips, err := server.TailscaleIPs(lookupCtx)
		if err != nil {
			return nil, fmt.Errorf("resolve tailscale IPs: %w", err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no tailscale IPs found on this host")
		}
		for _, ip := range ips {
			addrs = append(addrs, net.JoinHostPort(ip, port))
		}
	} else {
		addrs = []string{addr}
	}

	var listeners []net.Listener
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err != nil {
			for _, prev := range listeners {
				prev.Close()
			}
			return nil, listenError(a, err)
		}
		if tlsConfig != nil {
			l = tls.NewListener(l, tlsConfig)
		}
		listeners = append(listeners, l)
	}
	return listeners, nil
}

// listenError wraps a net.Listen error with a "held by pid N" hint when
// the address is in use and lsof can identify the holder.
func listenError(addr string, err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if se := new(os.SyscallError); errors.As(opErr.Err, &se) && se.Err == syscall.EADDRINUSE {
			if pid := portHolder(addr); pid != "" {
				return fmt.Errorf("listen %s: %w (held by pid %s)", addr, err, pid)
			}
		}
	}
	return fmt.Errorf("listen %s: %w", addr, err)
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
				Usage:   "listen address; use 'tailscale:<port>' to bind only this host's tailnet IPs",
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
				// When a human deploys exed out-of-band, update the CD topic.
				deployer.OnDeployDone(scheduler.NotifyDeploy)
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
					NextProtos:     []string{"h2", "http/1.1"},
				}
			}

			listeners, err := openListeners(ctx, c.String("addr"), srv.TLSConfig)
			if err != nil {
				return err
			}

			// Start git repo, inventory, and CD scheduler services.
			go gitRepo.Run(ctx)
			go inv.Run(ctx)
			if scheduler != nil {
				go scheduler.Run(ctx)
			}

			// Start server on each listener. http.Server.Serve is safe to
			// call concurrently from multiple goroutines on a single Server,
			// and Shutdown closes all of them.
			log.Info("server starting", "addr", c.String("addr"), "tls", useTLS, "version", version.Full())
			for _, l := range listeners {
				go func() {
					log.Info("listening", "addr", l.Addr())
					if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
						log.Error("server error", "addr", l.Addr(), "error", err)
						cancel()
					}
				}()
			}

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
