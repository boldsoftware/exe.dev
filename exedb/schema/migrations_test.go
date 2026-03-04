package schema_test

import (
	"embed"
	"regexp"
	"strings"
	"testing"
)

//go:embed *.sql
var migrationFS embed.FS

func TestMigrationFilenamesAreUnique(t *testing.T) {
	// Read all migration files
	entries, err := migrationFS.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read schema directory: %v", err)
	}

	// Track migration filenames
	migrationFiles := make(map[string]bool)
	migrationPattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Check filename format
		matches := migrationPattern.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			t.Errorf("migration file %s does not follow naming convention XXX-description.sql", entry.Name())
			continue
		}

		// Check for duplicate filenames
		if migrationFiles[entry.Name()] {
			t.Errorf("duplicate migration filename: %s", entry.Name())
		} else {
			migrationFiles[entry.Name()] = true
		}
	}

	// Ensure we have at least the base migrations
	if len(migrationFiles) < 1 {
		t.Errorf("expected at least 1 migration file, found %d", len(migrationFiles))
	}

	// Check that we have at least one base migration (named XXX-base.sql)
	hasBase := false
	for name := range migrationFiles {
		if strings.HasSuffix(name, "-base.sql") {
			hasBase = true
			break
		}
	}
	if !hasBase {
		t.Error("missing base migration (XXX-base.sql)")
	}
}
