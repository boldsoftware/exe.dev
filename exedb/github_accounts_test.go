package exedb_test

import (
	"context"
	"strings"
	"testing"

	"exe.dev/exedb"
)

func TestGitHubUserTokenTimestampRoundTrip(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		AccessToken:           "ghu_test",
		RefreshToken:          "ghr_test",
		AccessTokenExpiresAt:  strPtr("2026-04-21 09:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-09-21 09:00:00"),
	})
	if err != nil {
		t.Fatalf("UpsertGitHubUserToken: %v", err)
	}

	tok, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "testuser",
	})
	if err != nil {
		t.Fatalf("GetGitHubUserToken: %v", err)
	}

	if tok.AccessTokenExpiresAt == nil {
		t.Fatal("AccessTokenExpiresAt is nil")
	}
	if !strings.HasPrefix(*tok.AccessTokenExpiresAt, "2026-04-21") {
		t.Errorf("AccessTokenExpiresAt = %q, want prefix 2026-04-21", *tok.AccessTokenExpiresAt)
	}
	if tok.RefreshTokenExpiresAt == nil {
		t.Fatal("RefreshTokenExpiresAt is nil")
	}
	if !strings.HasPrefix(*tok.RefreshTokenExpiresAt, "2026-09-21") {
		t.Errorf("RefreshTokenExpiresAt = %q, want prefix 2026-09-21", *tok.RefreshTokenExpiresAt)
	}
}

func TestGitHubUserTokenUpdateRoundTrip(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:       userID,
		GitHubLogin:  "testuser",
		AccessToken:  "ghu_old",
		RefreshToken: "ghr_old",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpdateGitHubUserToken(ctx, exedb.UpdateGitHubUserTokenParams{
		AccessToken:           "ghu_new",
		RefreshToken:          "ghr_new",
		AccessTokenExpiresAt:  strPtr("2026-05-01 12:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-11-01 12:00:00"),
		UserID:                userID,
		GitHubLogin:           "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}

	if tok.AccessToken != "ghu_new" {
		t.Errorf("AccessToken = %q, want ghu_new", tok.AccessToken)
	}
	if tok.AccessTokenExpiresAt == nil || !strings.HasPrefix(*tok.AccessTokenExpiresAt, "2026-05-01") {
		t.Errorf("AccessTokenExpiresAt = %v, want prefix 2026-05-01", tok.AccessTokenExpiresAt)
	}
	if tok.RefreshTokenExpiresAt == nil || !strings.HasPrefix(*tok.RefreshTokenExpiresAt, "2026-11-01") {
		t.Errorf("RefreshTokenExpiresAt = %v, want prefix 2026-11-01", tok.RefreshTokenExpiresAt)
	}
}

func TestGitHubUserTokenNilTimestamps(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		AccessToken:           "ghu_test",
		RefreshToken:          "",
		AccessTokenExpiresAt:  nil,
		RefreshTokenExpiresAt: nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}

	if tok.AccessTokenExpiresAt != nil {
		t.Errorf("AccessTokenExpiresAt = %v, want nil", tok.AccessTokenExpiresAt)
	}
	if tok.RefreshTokenExpiresAt != nil {
		t.Errorf("RefreshTokenExpiresAt = %v, want nil", tok.RefreshTokenExpiresAt)
	}
}

func TestGitHubInstallationsCRUD(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Create the token first (FK constraint).
	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:       userID,
		GitHubLogin:  "testuser",
		AccessToken:  "ghu_test",
		RefreshToken: "ghr_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add two installations.
	for _, inst := range []struct {
		ID    int64
		Login string
	}{{12345, "personal"}, {67890, "org"}} {
		err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
			UserID:                  userID,
			GitHubLogin:             "testuser",
			GitHubAppInstallationID: inst.ID,
			GitHubAccountLogin:      inst.Login,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// List installations.
	installs, err := queries.ListGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 2 {
		t.Fatalf("got %d installations, want 2", len(installs))
	}

	// Get by target.
	inst, err := queries.GetGitHubInstallationByTarget(ctx, exedb.GetGitHubInstallationByTargetParams{
		UserID:             userID,
		GitHubAccountLogin: "org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.GitHubAppInstallationID != 67890 {
		t.Errorf("GitHubAppInstallationID = %d, want 67890", inst.GitHubAppInstallationID)
	}

	// Delete one installation.
	err = queries.DeleteGitHubInstallation(ctx, exedb.DeleteGitHubInstallationParams{
		UserID:                  userID,
		GitHubAppInstallationID: 12345,
	})
	if err != nil {
		t.Fatal(err)
	}

	installs, err = queries.ListGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 1 {
		t.Fatalf("got %d installations, want 1", len(installs))
	}
	if installs[0].GitHubAppInstallationID != 67890 {
		t.Errorf("remaining GitHubAppInstallationID = %d, want 67890", installs[0].GitHubAppInstallationID)
	}

	// Delete all installations.
	err = queries.DeleteAllGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	installs, err = queries.ListGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 0 {
		t.Fatalf("got %d installations, want 0", len(installs))
	}
}

func TestGitHubMultipleGitHubAccounts(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Two different GitHub accounts for the same exe user.
	for _, gh := range []struct {
		Login string
		Token string
	}{{"phil", "ghu_phil"}, {"phil-work", "ghu_phil_work"}} {
		err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
			UserID:       userID,
			GitHubLogin:  gh.Login,
			AccessToken:  gh.Token,
			RefreshToken: "ghr_" + gh.Login,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Both tokens should exist independently.
	tokens, err := queries.ListGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(tokens))
	}

	// Each has its own access token.
	tok1, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "phil",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok1.AccessToken != "ghu_phil" {
		t.Errorf("tok1.AccessToken = %q, want ghu_phil", tok1.AccessToken)
	}

	tok2, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "phil-work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok2.AccessToken != "ghu_phil_work" {
		t.Errorf("tok2.AccessToken = %q, want ghu_phil_work", tok2.AccessToken)
	}

	// Updating one doesn't affect the other.
	err = queries.UpdateGitHubUserToken(ctx, exedb.UpdateGitHubUserTokenParams{
		AccessToken:  "ghu_phil_refreshed",
		RefreshToken: "ghr_phil_refreshed",
		UserID:       userID,
		GitHubLogin:  "phil",
	})
	if err != nil {
		t.Fatal(err)
	}

	tok2Again, err := queries.GetGitHubUserToken(ctx, exedb.GetGitHubUserTokenParams{
		UserID: userID, GitHubLogin: "phil-work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok2Again.AccessToken != "ghu_phil_work" {
		t.Errorf("tok2 was modified: %q", tok2Again.AccessToken)
	}
}

func TestGitHubDeleteInstallationsThenTokens(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:       userID,
		GitHubLogin:  "testuser",
		AccessToken:  "ghu_test",
		RefreshToken: "ghr_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
		UserID:                  userID,
		GitHubLogin:             "testuser",
		GitHubAppInstallationID: 12345,
		GitHubAccountLogin:      "personal",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delete installations first, then tokens.
	err = queries.DeleteAllGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	err = queries.DeleteAllGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}

	installs, err := queries.ListGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 0 {
		t.Fatalf("got %d installations after delete, want 0", len(installs))
	}

	tokens, err := queries.ListGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("got %d tokens after delete, want 0", len(tokens))
	}
}

func TestListGitHubUserTokensNeedingRenewal(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		AccessToken:           "ghu_test",
		RefreshToken:          "ghr_test",
		AccessTokenExpiresAt:  strPtr("2026-03-30 00:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-03-30 00:00:00"),
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := queries.ListGitHubUserTokensNeedingRenewal(ctx, 10)
	if err != nil {
		t.Fatalf("ListGitHubUserTokensNeedingRenewal: %v", err)
	}

	if len(tokens) != 1 {
		t.Fatalf("got %d tokens needing renewal, want 1", len(tokens))
	}
	if tokens[0].UserID != userID {
		t.Errorf("UserID = %q, want %q", tokens[0].UserID, userID)
	}
}

func TestGitHubReinstallNewInstallationID(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:       userID,
		GitHubLogin:  "testuser",
		AccessToken:  "ghu_test",
		RefreshToken: "ghr_test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Original installation.
	err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
		UserID:                  userID,
		GitHubLogin:             "testuser",
		GitHubAppInstallationID: 11111,
		GitHubAccountLogin:      "myorg",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reinstall with a new installation ID on the same target.
	// First, delete the stale one (simulating what saveGitHubSetupWeb does).
	err = queries.DeleteGitHubInstallationByTarget(ctx, exedb.DeleteGitHubInstallationByTargetParams{
		UserID:                  userID,
		GitHubAccountLogin:      "myorg",
		GitHubAppInstallationID: 22222, // new ID
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now upsert the new installation — should succeed without UNIQUE violation.
	err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
		UserID:                  userID,
		GitHubLogin:             "testuser",
		GitHubAppInstallationID: 22222,
		GitHubAccountLogin:      "myorg",
	})
	if err != nil {
		t.Fatalf("UpsertGitHubInstallation after reinstall: %v", err)
	}

	installs, err := queries.ListGitHubInstallations(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 1 {
		t.Fatalf("got %d installations, want 1", len(installs))
	}
	if installs[0].GitHubAppInstallationID != 22222 {
		t.Errorf("GitHubAppInstallationID = %d, want 22222", installs[0].GitHubAppInstallationID)
	}
}

func TestGitHubDeleteOrphanedTokens(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Two GitHub accounts.
	for _, login := range []string{"personal", "work"} {
		err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
			UserID:       userID,
			GitHubLogin:  login,
			AccessToken:  "ghu_" + login,
			RefreshToken: "ghr_" + login,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Only "personal" has an installation.
	err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
		UserID:                  userID,
		GitHubLogin:             "personal",
		GitHubAppInstallationID: 12345,
		GitHubAccountLogin:      "personal",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delete orphaned tokens — "work" should be removed.
	err = queries.DeleteOrphanedGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatalf("DeleteOrphanedGitHubUserTokens: %v", err)
	}

	tokens, err := queries.ListGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	if tokens[0].GitHubLogin != "personal" {
		t.Errorf("remaining token GitHubLogin = %q, want personal", tokens[0].GitHubLogin)
	}
}

func TestGitHubDeleteOrphanedTokensKeepsAll(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Token with installation — should not be deleted.
	err = queries.UpsertGitHubUserToken(ctx, exedb.UpsertGitHubUserTokenParams{
		UserID:       userID,
		GitHubLogin:  "testuser",
		AccessToken:  "ghu_test",
		RefreshToken: "ghr_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = queries.UpsertGitHubInstallation(ctx, exedb.UpsertGitHubInstallationParams{
		UserID:                  userID,
		GitHubLogin:             "testuser",
		GitHubAppInstallationID: 12345,
		GitHubAccountLogin:      "testuser",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = queries.DeleteOrphanedGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := queries.ListGitHubUserTokens(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
}
