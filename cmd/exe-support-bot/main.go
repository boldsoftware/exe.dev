// exe-support-bot pulls Missive support conversations into a local SQLite
// database and runs a Claude Sonnet agent loop over them that can publish
// results to its own web page.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: exe-support-bot [-db path] <subcommand> [flags]

subcommands:
  import    Pull conversations from Missive into the local SQLite DB
  run       Run the agent loop against a single conversation or ad-hoc prompt
  serve     Serve the web UI & API
  schema    Print the SQLite schema
  judge-backtest  Run the pre-pass judge over the last N conversations and write a TSV
`)
}

func main() {
	globalFlags := flag.NewFlagSet("exe-support-bot", flag.ExitOnError)
	dbPath := globalFlags.String("db", "/data/support-bot.db", "SQLite database path")
	globalFlags.Usage = usage
	if err := globalFlags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	rest := globalFlags.Args()
	if len(rest) == 0 {
		usage()
		os.Exit(2)
	}
	sub := rest[0]
	subArgs := rest[1:]

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch sub {
	case "import":
		if err := runImport(ctx, *dbPath, subArgs); err != nil {
			fmt.Fprintf(os.Stderr, "import: %v\n", err)
			os.Exit(1)
		}
	case "run":
		if err := runAgentCLI(ctx, *dbPath, subArgs); err != nil {
			fmt.Fprintf(os.Stderr, "run: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(ctx, *dbPath, subArgs); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	case "judge-backtest":
		if err := runJudgeBacktest(ctx, *dbPath, subArgs); err != nil {
			fmt.Fprintf(os.Stderr, "judge-backtest: %v\n", err)
			os.Exit(1)
		}
	case "schema":
		fmt.Print(dbSchemaSQL)
	default:
		usage()
		os.Exit(2)
	}
}
