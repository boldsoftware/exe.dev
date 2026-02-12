package exedb

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"exe.dev/sqlite"
	_ "modernc.org/sqlite"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func BenchmarkMigrateFromScratch(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		dir := b.TempDir()
		dbPath := filepath.Join(dir, "test.sqlite3")
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			b.Fatal(err)
		}
		if err := RunMigrations(discardLogger(), rawDB); err != nil {
			b.Fatal(err)
		}
		rawDB.Close()
		db, err := sqlite.New(dbPath, 4)
		if err != nil {
			b.Fatal(err)
		}
		db.Close()
	}
}

func BenchmarkCopyTemplateDB(b *testing.B) {
	// Prime the template cache.
	if _, err := TemplateDBPath(discardLogger()); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		dir := b.TempDir()
		dbPath := filepath.Join(dir, "test.sqlite3")
		if err := CopyTemplateDB(discardLogger(), dbPath); err != nil {
			b.Fatal(err)
		}
		db, err := sqlite.New(dbPath, 4)
		if err != nil {
			b.Fatal(err)
		}
		db.Close()
	}
}
