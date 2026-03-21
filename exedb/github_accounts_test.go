package exedb_test

import (
	"context"
	"strings"
	"testing"

	"exe.dev/exedb"
)

func TestGitHubAccountTimestampRoundTrip(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		InstallationID:        12345,
		TargetLogin:           "testorg",
		AccessToken:           "ghu_test",
		RefreshToken:          "ghr_test",
		AccessTokenExpiresAt:  strPtr("2026-04-21 09:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-09-21 09:00:00"),
	})
	if err != nil {
		t.Fatalf("UpsertGitHubAccount: %v", err)
	}

	acct, err := queries.GetGitHubAccount(ctx, exedb.GetGitHubAccountParams{
		UserID:         userID,
		InstallationID: 12345,
	})
	if err != nil {
		t.Fatalf("GetGitHubAccount: %v", err)
	}

	if acct.AccessTokenExpiresAt == nil {
		t.Fatal("AccessTokenExpiresAt is nil")
	}
	// The driver may reformat; just check the date is preserved.
	if !strings.HasPrefix(*acct.AccessTokenExpiresAt, "2026-04-21") {
		t.Errorf("AccessTokenExpiresAt = %q, want prefix 2026-04-21", *acct.AccessTokenExpiresAt)
	}
	if acct.RefreshTokenExpiresAt == nil {
		t.Fatal("RefreshTokenExpiresAt is nil")
	}
	if !strings.HasPrefix(*acct.RefreshTokenExpiresAt, "2026-09-21") {
		t.Errorf("RefreshTokenExpiresAt = %q, want prefix 2026-09-21", *acct.RefreshTokenExpiresAt)
	}
}

func TestGitHubAccountUpdateTokensRoundTrip(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
		UserID:         userID,
		GitHubLogin:    "testuser",
		InstallationID: 12345,
		TargetLogin:    "testorg",
		AccessToken:    "ghu_old",
		RefreshToken:   "ghr_old",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpdateGitHubAccountTokens(ctx, exedb.UpdateGitHubAccountTokensParams{
		AccessToken:           "ghu_new",
		RefreshToken:          "ghr_new",
		AccessTokenExpiresAt:  strPtr("2026-05-01 12:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-11-01 12:00:00"),
		UserID:                userID,
		InstallationID:        12345,
	})
	if err != nil {
		t.Fatal(err)
	}

	acct, err := queries.GetGitHubAccount(ctx, exedb.GetGitHubAccountParams{
		UserID:         userID,
		InstallationID: 12345,
	})
	if err != nil {
		t.Fatal(err)
	}

	if acct.AccessToken != "ghu_new" {
		t.Errorf("AccessToken = %q, want ghu_new", acct.AccessToken)
	}
	if acct.AccessTokenExpiresAt == nil || !strings.HasPrefix(*acct.AccessTokenExpiresAt, "2026-05-01") {
		t.Errorf("AccessTokenExpiresAt = %v, want prefix 2026-05-01", acct.AccessTokenExpiresAt)
	}
	if acct.RefreshTokenExpiresAt == nil || !strings.HasPrefix(*acct.RefreshTokenExpiresAt, "2026-11-01") {
		t.Errorf("RefreshTokenExpiresAt = %v, want prefix 2026-11-01", acct.RefreshTokenExpiresAt)
	}
}

func TestGitHubAccountLegacyTimestampScan(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// Insert the exact broken format from production.
	// The migration already ran but our data is inserted after, simulating
	// what legacy rows look like after parse_timestamp() normalizes them.
	// We also test that even unnormalized strings scan fine as *string.
	_, err = db.ExecContext(ctx, `INSERT INTO github_accounts
		(user_id, github_login, installation_id, target_login, access_token, refresh_token,
		 access_token_expires_at, refresh_token_expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, "testuser", 12345, "testorg", "ghu_test", "ghr_test",
		"2026-03-21 09:28:42.227245564 +0000 UTC m=+29529.384844575",
		"2026-09-21 09:28:42.227245564 +0000 UTC m=+29529.384844575",
	)
	if err != nil {
		t.Fatal(err)
	}

	// With *string columns, any string scans fine — no more "unsupported Scan" error.
	acct, err := queries.GetGitHubAccount(ctx, exedb.GetGitHubAccountParams{
		UserID:         userID,
		InstallationID: 12345,
	})
	if err != nil {
		t.Fatalf("GetGitHubAccount with legacy timestamps: %v", err)
	}

	if acct.AccessTokenExpiresAt == nil {
		t.Fatal("AccessTokenExpiresAt is nil")
	}
	// Should contain the date portion regardless of format.
	if !strings.Contains(*acct.AccessTokenExpiresAt, "2026") {
		t.Errorf("AccessTokenExpiresAt = %q, expected to contain 2026", *acct.AccessTokenExpiresAt)
	}
	t.Logf("legacy AccessTokenExpiresAt = %q", *acct.AccessTokenExpiresAt)
}

func TestGitHubAccountNilTimestamps(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		InstallationID:        12345,
		TargetLogin:           "testorg",
		AccessToken:           "ghu_test",
		RefreshToken:          "",
		AccessTokenExpiresAt:  nil,
		RefreshTokenExpiresAt: nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	acct, err := queries.GetGitHubAccount(ctx, exedb.GetGitHubAccountParams{
		UserID:         userID,
		InstallationID: 12345,
	})
	if err != nil {
		t.Fatal(err)
	}

	if acct.AccessTokenExpiresAt != nil {
		t.Errorf("AccessTokenExpiresAt = %v, want nil", acct.AccessTokenExpiresAt)
	}
	if acct.RefreshTokenExpiresAt != nil {
		t.Errorf("RefreshTokenExpiresAt = %v, want nil", acct.RefreshTokenExpiresAt)
	}
}

func TestListGitHubAccountsNeedingRenewal(t *testing.T) {
	db, queries := setupTestDB(t)
	ctx := context.Background()

	userID := "usrTEST123456789"
	_, err := db.ExecContext(ctx, `INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
		UserID:                userID,
		GitHubLogin:           "testuser",
		InstallationID:        12345,
		TargetLogin:           "testorg",
		AccessToken:           "ghu_test",
		RefreshToken:          "ghr_test",
		AccessTokenExpiresAt:  strPtr("2026-03-30 00:00:00"),
		RefreshTokenExpiresAt: strPtr("2026-03-30 00:00:00"),
	})
	if err != nil {
		t.Fatal(err)
	}

	accounts, err := queries.ListGitHubAccountsNeedingRenewal(ctx, 10)
	if err != nil {
		t.Fatalf("ListGitHubAccountsNeedingRenewal: %v", err)
	}

	if len(accounts) != 1 {
		t.Fatalf("got %d accounts needing renewal, want 1", len(accounts))
	}
	if accounts[0].UserID != userID {
		t.Errorf("UserID = %q, want %q", accounts[0].UserID, userID)
	}
}
