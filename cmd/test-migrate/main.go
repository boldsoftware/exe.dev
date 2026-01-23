package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"exe.dev/exedb"

	_ "modernc.org/sqlite"
)

func main() {
	copyFlag := flag.Bool("c", false, "copy database to a temp file before migrating (leaves original untouched)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: test-migrate [-c] <path-to-exe.db>\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Runs all pending migrations on the specified database.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Use this to test migrations against a copy of a production database.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	dbPath := flag.Arg(0)

	if *copyFlag {
		tempPath, err := copyToTemp(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "copied to %s\n", tempPath)
		dbPath = tempPath
	}

	if err := runMigrations(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runMigrations(dbPath string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("cannot access database file: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Checkpoint WAL before running migrations.
	if _, err := db.Exec("PRAGMA wal_checkpoint(FULL)"); err != nil {
		return fmt.Errorf("failed to checkpoint WAL: %w", err)
	}

	// Set busy_timeout to handle database lock contention.
	if _, err := db.Exec("PRAGMA busy_timeout=1000"); err != nil {
		return fmt.Errorf("failed to set busy_timeout: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	if err := exedb.RunMigrations(logger, db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	fmt.Println("migrations complete")
	return nil
}

func copyToTemp(srcPath string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("cannot open source database: %w", err)
	}
	defer src.Close()

	base := filepath.Base(srcPath)
	dst, err := os.CreateTemp("", base+"-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		os.Remove(dst.Name())
		return "", fmt.Errorf("cannot copy database: %w", err)
	}

	return dst.Name(), nil
}
