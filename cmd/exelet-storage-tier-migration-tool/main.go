package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	cli "github.com/urfave/cli/v2"

	"exe.dev/exelet/client"
)

func main() {
	app := cli.NewApp()
	app.Name = "exelet-storage-tier-migration-tool"
	app.Usage = "Continuously migrate instances between storage tiers for stress testing"
	app.Flags = []cli.Flag{
		&cli.StringSliceFlag{
			Name:     "addr",
			Aliases:  []string{"a"},
			Usage:    "exelet GRPC address (repeatable)",
			Required: true,
			EnvVars:  []string{"EXELET_ADDR"},
		},
		&cli.DurationFlag{
			Name:    "cooldown",
			Aliases: []string{"c"},
			Usage:   "cooldown period between migrations",
			Value:   30 * time.Second,
		},
		&cli.BoolFlag{
			Name:  "live",
			Usage: "use live migration (near-zero downtime for running VMs)",
		},
		&cli.StringFlag{
			Name:    "report",
			Aliases: []string{"r"},
			Usage:   "path to write JSON report on exit",
		},
		&cli.StringSliceFlag{
			Name:    "pools",
			Aliases: []string{"p"},
			Usage:   "allowed target pools (never 'backup')",
			Value:   cli.NewStringSlice("tank", "dozer"),
		},
		&cli.IntFlag{
			Name:    "max-migrations",
			Aliases: []string{"n"},
			Usage:   "stop after N migrations (0 = unlimited)",
		},
		&cli.BoolFlag{
			Name:    "debug",
			Aliases: []string{"D"},
			Usage:   "enable debug logging",
		},
	}
	app.Action = run

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(clix *cli.Context) error {
	// Setup logging
	logLevel := slog.LevelInfo
	if clix.Bool("debug") {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(handler))

	// Validate pools
	pools := clix.StringSlice("pools")
	for _, p := range pools {
		if strings.EqualFold(p, "backup") {
			return fmt.Errorf("pool 'backup' is not allowed as a migration target")
		}
	}
	if len(pools) == 0 {
		return fmt.Errorf("at least one pool must be specified")
	}

	// Connect to exelets
	addrs := clix.StringSlice("addr")
	targets := make([]exeletTarget, 0, len(addrs))
	for _, addr := range addrs {
		c, err := client.NewClient(addr, client.WithInsecure())
		if err != nil {
			return fmt.Errorf("connect to %s: %w", addr, err)
		}
		defer c.Close()
		targets = append(targets, exeletTarget{addr: addr, client: c})
	}

	slog.Info("connected to exelets", "count", len(targets))

	collector := NewReportCollector()
	migrator := NewMigrator(
		targets,
		pools,
		clix.Bool("live"),
		clix.Duration("cooldown"),
		clix.Int("max-migrations"),
		collector,
	)

	// Build inventory
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slog.InfoContext(ctx, "building instance inventory...")
	if err := migrator.BuildInventory(ctx); err != nil {
		return fmt.Errorf("build inventory: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start migration loop
	migrationCtx, migrationCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		migrator.Run(migrationCtx)
	}()

	// Start TUI dashboard
	p := tea.NewProgram(
		newDashboardModel(migrator, collector),
		tea.WithAltScreen(),
	)

	// Signal handler goroutine
	go func() {
		select {
		case <-sigCh:
			slog.InfoContext(ctx, "shutting down...")
			migrationCancel()
			// Wait for migration loop to finish
			<-doneCh
			p.Send(doneMsg{})
		case <-doneCh:
			// Migration loop finished (max-migrations reached)
			p.Send(doneMsg{})
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}

	// Ensure migration loop is stopped
	migrationCancel()
	<-doneCh

	// Print final summary
	collector.PrintSummary()

	// Write report if requested
	if reportPath := clix.String("report"); reportPath != "" {
		if err := collector.WriteJSON(reportPath); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Printf("\nReport written to %s\n", reportPath)
	}

	return nil
}
