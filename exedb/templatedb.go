package exedb

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

// SchemaHash returns a short hex hash of all embedded schema files.
// This is used as a cache key for the template database.
func SchemaHash() string {
	h := sha256.New()
	entries, err := migrationFS.ReadDir("schema")
	if err != nil {
		panic(fmt.Sprintf("exedb: reading embedded schema: %v", err))
	}
	for _, e := range entries {
		content, err := migrationFS.ReadFile("schema/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("exedb: reading embedded file %s: %v", e.Name(), err))
		}
		fmt.Fprintf(h, "%s:%d:", e.Name(), len(content))
		h.Write(content)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// TemplateDBPath returns the path to a pre-migrated SQLite database.
// The template is created once per process and cached on disk keyed by schema hash.
// Callers should copy this file rather than using it directly.
func TemplateDBPath(slog *slog.Logger) (string, error) {
	templateOnce.Do(func() {
		templatePath, templateErr = createTemplateDB(slog)
	})
	return templatePath, templateErr
}

func createTemplateDB(slog *slog.Logger) (string, error) {
	hash := SchemaHash()
	cacheDir := filepath.Join(os.TempDir(), "exedb-template")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("exedb: create template cache dir: %w", err)
	}

	dbPath := filepath.Join(cacheDir, hash+".sqlite3")

	// If a cached template already exists with this hash, verify it's usable.
	if _, err := os.Stat(dbPath); err == nil {
		if verifyTemplate(dbPath) == nil {
			slog.Debug("using cached template database", "path", dbPath, "hash", hash)
			return dbPath, nil
		}
		// Corrupted or incompatible; recreate.
		os.Remove(dbPath)
	}

	slog.Debug("creating template database", "path", dbPath, "hash", hash)

	// Create in a temp file and rename to avoid races between parallel test processes.
	tmp, err := os.CreateTemp(cacheDir, "template-*.sqlite3")
	if err != nil {
		return "", fmt.Errorf("exedb: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()

	rawDB, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("exedb: open temp db: %w", err)
	}

	if err := RunMigrations(slog, rawDB); err != nil {
		rawDB.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("exedb: run migrations for template: %w", err)
	}

	// Checkpoint WAL so the DB is a single self-contained file.
	if _, err := rawDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		rawDB.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("exedb: checkpoint template: %w", err)
	}
	rawDB.Close()

	// Atomic rename.
	if err := os.Rename(tmpPath, dbPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("exedb: rename template: %w", err)
	}

	return dbPath, nil
}

// verifyTemplate opens the DB and checks that the migrations table exists and is non-empty.
func verifyTemplate(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM migrations").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("empty migrations table")
	}
	return nil
}

// CopyTemplateDB copies the pre-migrated template database to dst.
// This is much faster than running migrations from scratch.
func CopyTemplateDB(slog *slog.Logger, dst string) error {
	src, err := TemplateDBPath(slog)
	if err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("exedb: open template: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("exedb: create dst: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("exedb: copy template: %w", err)
	}
	return out.Close()
}
