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
	"exe.dev/exe-ops/server/aiagent"
	"exe.dev/exe-ops/server/exed"
	"exe.dev/exe-ops/ui"
	"exe.dev/exe-ops/version"
	"github.com/tailscale/tscert"
	"github.com/urfave/cli/v2"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	app := &cli.App{
		Name:    "exe-ops-server",
		Usage:   "Infrastructure monitoring server for exe-ops",
		Version: version.Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Usage:   "listen address",
				Value:   ":8080",
				EnvVars: []string{"EXE_OPS_ADDR"},
			},
			&cli.StringFlag{
				Name:    "db",
				Usage:   "SQLite database path",
				Value:   "exe-ops.db",
				EnvVars: []string{"EXE_OPS_DB"},
			},
			&cli.StringFlag{
				Name:     "token",
				Usage:    "authentication token for agent connections",
				Required: true,
				EnvVars:  []string{"EXE_OPS_TOKEN"},
			},
			&cli.DurationFlag{
				Name:    "retention",
				Usage:   "data retention duration",
				Value:   168 * time.Hour, // 7 days
				EnvVars: []string{"EXE_OPS_RETENTION"},
			},
			&cli.BoolFlag{
				Name:    "tls",
				Usage:   "enable TLS via Tailscale (requires tailscaled)",
				Value:   true,
				EnvVars: []string{"EXE_OPS_TLS"},
			},
			&cli.StringFlag{
				Name:    "ai-provider",
				Usage:   "AI provider (anthropic, openai, openai-compat, ollama)",
				EnvVars: []string{"EXE_OPS_AI_PROVIDER"},
			},
			&cli.StringFlag{
				Name:    "ai-api-key",
				Usage:   "API key for the AI provider",
				EnvVars: []string{"EXE_OPS_AI_API_KEY"},
			},
			&cli.StringFlag{
				Name:    "ai-model",
				Usage:   "AI model name",
				EnvVars: []string{"EXE_OPS_AI_MODEL"},
			},
			&cli.StringFlag{
				Name:    "ai-base-url",
				Usage:   "Base URL for the AI provider API",
				EnvVars: []string{"EXE_OPS_AI_BASE_URL"},
			},
			&cli.StringSliceFlag{
				Name:    "exed",
				Usage:   "exed environment in env:base-url format, e.g. prod:https://exed.example.com (repeatable; comma-separated in env var); if omitted, defaults to local at http://localhost:8080",
				EnvVars: []string{"EXE_OPS_EXED"},
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

			db, err := server.OpenDB(c.String("db"))
			if err != nil {
				return err
			}
			defer db.Close()

			store := server.NewStore(db)
			hub := server.NewHub(log)
			uiFS := ui.FS()

			// Initialize AI provider if configured.
			var ai aiagent.Provider
			var aiCfg *aiagent.Config
			if providerName := c.String("ai-provider"); providerName != "" {
				aiCfg = &aiagent.Config{
					Provider: providerName,
					APIKey:   c.String("ai-api-key"),
					Model:    c.String("ai-model"),
					BaseURL:  c.String("ai-base-url"),
				}
				var err error
				ai, err = aiagent.NewProvider(aiCfg)
				if err != nil {
					return fmt.Errorf("init AI provider: %w", err)
				}
				log.Info("AI agent enabled", "provider", aiCfg.Provider, "model", aiCfg.Model, "base_url", aiCfg.BaseURL)
			}

			// Initialize exed client.
			exedCfg, err := exed.ParseFlags(c.StringSlice("exed"))
			if err != nil {
				return fmt.Errorf("parse --exed flags: %w", err)
			}
			exedClient := exed.NewClient(exedCfg)
			log.Info("exed environments configured", "envs", exedClient.Envs())

			handler := server.New(store, hub, c.String("token"), uiFS, log, ai, aiCfg, exedClient)

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

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

			// Start retention purge goroutine.
			retention := c.Duration("retention")
			go func() {
				ticker := time.NewTicker(1 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						deleted, err := store.PurgeOldReports(ctx, retention)
						if err != nil {
							log.Error("purge failed", "error", err)
						} else if deleted > 0 {
							log.Info("purged old reports", "deleted", deleted)
						}
						chatDeleted, err := store.PurgeOldConversations(ctx, 30*24*time.Hour)
						if err != nil {
							log.Error("chat purge failed", "error", err)
						} else if chatDeleted > 0 {
							log.Info("purged old conversations", "deleted", chatDeleted)
						}
						capDeleted, err := store.PurgeOldExeletCapacity(ctx, retention)
						if err != nil {
							log.Error("exelet capacity purge failed", "error", err)
						} else if capDeleted > 0 {
							log.Info("purged old exelet capacity", "deleted", capDeleted)
						}
					}
				}
			}()

			// Start exelet capacity collection goroutine.
			go func() {
				collectExeletCapacity(ctx, log, exedClient, store)
				ticker := time.NewTicker(60 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						collectExeletCapacity(ctx, log, exedClient, store)
					}
				}
			}()

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

func collectExeletCapacity(ctx context.Context, log *slog.Logger, client *exed.Client, store *server.Store) {
	now := time.Now()
	results := client.FetchAll(ctx)
	for _, r := range results {
		if r.Error != "" {
			log.Warn("exed fetch failed", "env", r.Env, "error", r.Error)
			continue
		}
		exelets := r.ParseExelets()
		if len(exelets) == 0 {
			continue
		}
		entries := make([]server.ExeletCapacityEntry, 0, len(exelets))
		for _, e := range exelets {
			entries = append(entries, server.ExeletCapacityEntry{
				ServerName: e.Hostname,
				Instances:  e.Instances,
				Capacity:   e.Capacity,
			})
		}
		if err := store.InsertExeletCapacity(ctx, r.Env, now, entries); err != nil {
			log.Error("store exelet capacity", "env", r.Env, "error", err)
		}
	}
}
