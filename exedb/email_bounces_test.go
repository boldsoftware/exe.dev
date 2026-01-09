package exedb_test

import (
	"context"
	"testing"

	"exe.dev/exedb"
)

func TestEmailBounces(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()

	// Initially, email should not be bounced
	bounced, err := queries.IsEmailBounced(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("IsEmailBounced failed: %v", err)
	}
	if bounced != 0 {
		t.Errorf("expected email to not be bounced, got %d", bounced)
	}

	// Insert a bounce record
	err = queries.InsertEmailBounce(ctx, exedb.InsertEmailBounceParams{
		Email:  "test@example.com",
		Reason: "406 marked as inactive",
	})
	if err != nil {
		t.Fatalf("InsertEmailBounce failed: %v", err)
	}

	// Now email should be bounced
	bounced, err = queries.IsEmailBounced(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("IsEmailBounced failed: %v", err)
	}
	if bounced != 1 {
		t.Errorf("expected email to be bounced, got %d", bounced)
	}

	// Different email should not be bounced
	bounced, err = queries.IsEmailBounced(ctx, "other@example.com")
	if err != nil {
		t.Fatalf("IsEmailBounced failed: %v", err)
	}
	if bounced != 0 {
		t.Errorf("expected other email to not be bounced, got %d", bounced)
	}

	// Can retrieve bounce details
	bounce, err := queries.GetEmailBounce(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("GetEmailBounce failed: %v", err)
	}
	if bounce.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", bounce.Email)
	}
	if bounce.Reason != "406 marked as inactive" {
		t.Errorf("expected reason '406 marked as inactive', got %s", bounce.Reason)
	}

	// Insert/replace updates the reason
	err = queries.InsertEmailBounce(ctx, exedb.InsertEmailBounceParams{
		Email:  "test@example.com",
		Reason: "updated reason",
	})
	if err != nil {
		t.Fatalf("InsertEmailBounce (update) failed: %v", err)
	}

	bounce, err = queries.GetEmailBounce(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("GetEmailBounce after update failed: %v", err)
	}
	if bounce.Reason != "updated reason" {
		t.Errorf("expected updated reason, got %s", bounce.Reason)
	}
}
