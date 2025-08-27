package schema_test

import (
	"embed"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

//go:embed *.sql
var migrationFS embed.FS

func TestMigrationNumbersAreUnique(t *testing.T) {
	// Read all migration files
	entries, err := migrationFS.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read schema directory: %v", err)
	}

	// Track migration numbers
	migrationNumbers := make(map[int]string)
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

		// Parse migration number
		migrationNumber, err := strconv.Atoi(matches[1])
		if err != nil {
			t.Errorf("failed to parse migration number from %s: %v", entry.Name(), err)
			continue
		}

		// Check range (001-999)
		if migrationNumber < 1 || migrationNumber > 999 {
			t.Errorf("migration number %d in file %s is outside valid range 001-999", migrationNumber, entry.Name())
			continue
		}

		// Check for duplicates
		if existingFile, exists := migrationNumbers[migrationNumber]; exists {
			t.Errorf("duplicate migration number %d found in files %s and %s", migrationNumber, existingFile, entry.Name())
		} else {
			migrationNumbers[migrationNumber] = entry.Name()
		}
	}

	// Ensure we have at least the base migrations
	if len(migrationNumbers) < 1 {
		t.Errorf("expected at least 1 migration file, found %d", len(migrationNumbers))
	}

	// Check that we have 001
	if _, exists := migrationNumbers[1]; !exists {
		t.Error("missing base migration 001-base.sql")
	}
}
