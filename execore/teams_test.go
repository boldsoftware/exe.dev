package execore

import (
	"context"
	"testing"

	"exe.dev/exedb"
)

func TestAddTeamMemberSetsParentID(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	ownerID := createTestUser(t, server, "owner@parent-test.example")
	memberID := createTestUser(t, server, "member@parent-test.example")

	// Create team with billing owner.
	teamID := "tm_parentid_test"
	err := withTx1(server, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID: teamID, DisplayName: "ParentID Test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.addTeamMember(ctx, teamID, ownerID, "billing_owner"); err != nil {
		t.Fatal(err)
	}

	// Billing owner should NOT have parent_id.
	ownerAcct, err := withRxRes1(server, ctx, (*exedb.Queries).GetAccountByUserID, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerAcct.ParentID != nil {
		t.Fatalf("billing owner should have nil parent_id, got %v", *ownerAcct.ParentID)
	}

	// Add member — should set parent_id to owner's account.
	if err := server.addTeamMember(ctx, teamID, memberID, "user"); err != nil {
		t.Fatal(err)
	}
	memberAcct, err := withRxRes1(server, ctx, (*exedb.Queries).GetAccountByUserID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if memberAcct.ParentID == nil || *memberAcct.ParentID != ownerAcct.ID {
		t.Fatalf("member parent_id = %v, want %q", memberAcct.ParentID, ownerAcct.ID)
	}

	// Remove member — should clear parent_id.
	if err := server.deleteTeamMember(ctx, teamID, memberID); err != nil {
		t.Fatal(err)
	}
	memberAcct, err = withRxRes1(server, ctx, (*exedb.Queries).GetAccountByUserID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if memberAcct.ParentID != nil {
		t.Fatalf("after removal, member parent_id = %v, want nil", *memberAcct.ParentID)
	}
}

func TestResolveTeamShardCollisions(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	// Create two users.
	aliceID := createTestUser(t, server, "alice@shard-test.example")
	bobID := createTestUser(t, server, "bob@shard-test.example")

	// Create a box for each user (noShard), then manually assign both to shard 1.
	aliceBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        aliceID,
		ctrhost:       "tcp://fake:9080",
		name:          "alice-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	bobBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        bobID,
		ctrhost:       "tcp://fake:9080",
		name:          "bob-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Manually assign both boxes to shard 1 (simulating pre-team allocation).
	err = withTx1(server, ctx, (*exedb.Queries).InsertBoxIPShard, exedb.InsertBoxIPShardParams{
		BoxID:   aliceBoxID,
		UserID:  aliceID,
		IPShard: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withTx1(server, ctx, (*exedb.Queries).InsertBoxIPShard, exedb.InsertBoxIPShardParams{
		BoxID:   bobBoxID,
		UserID:  bobID,
		IPShard: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify both are on shard 1.
	aliceShard, err := withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, aliceBoxID)
	if err != nil {
		t.Fatal(err)
	}
	bobShard, err := withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, bobBoxID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceShard != 1 || bobShard != 1 {
		t.Fatalf("expected both on shard 1, got alice=%d bob=%d", aliceShard, bobShard)
	}

	// Create team, add Alice as admin.
	teamID := "tm_shardtest"
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID:      teamID,
		DisplayName: "ShardTest",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: aliceID,
		Role:   "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add Bob to the team — this triggers resolveTeamShardCollisions.
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: bobID,
		Role:   "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.resolveTeamShardCollisions(ctx, teamID, bobID)

	// Verify: Alice stays on shard 1, Bob moved to a different shard.
	aliceShard, err = withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, aliceBoxID)
	if err != nil {
		t.Fatal(err)
	}
	bobShard, err = withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, bobBoxID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceShard != 1 {
		t.Errorf("alice's shard changed: got %d, want 1", aliceShard)
	}
	if bobShard == 1 {
		t.Errorf("bob's shard was not reassigned: still on shard 1")
	}
	if bobShard == aliceShard {
		t.Errorf("bob and alice still on same shard: %d", bobShard)
	}
	t.Logf("alice shard=%d, bob shard=%d (was 1, reassigned to %d)", aliceShard, bobShard, bobShard)
}

func TestResolveTeamShardCollisions_NoCollision(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	aliceID := createTestUser(t, server, "alice@no-collision.example")
	bobID := createTestUser(t, server, "bob@no-collision.example")

	aliceBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        aliceID,
		ctrhost:       "tcp://fake:9080",
		name:          "alice-nc-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	bobBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        bobID,
		ctrhost:       "tcp://fake:9080",
		name:          "bob-nc-vm",
		image:         "ubuntu:latest",
		noShard:       true,
		region:        "pdx",
		allocatedCPUs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Assign to different shards (no collision).
	err = withTx1(server, ctx, (*exedb.Queries).InsertBoxIPShard, exedb.InsertBoxIPShardParams{
		BoxID: aliceBoxID, UserID: aliceID, IPShard: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withTx1(server, ctx, (*exedb.Queries).InsertBoxIPShard, exedb.InsertBoxIPShardParams{
		BoxID: bobBoxID, UserID: bobID, IPShard: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create team, add both.
	teamID := "tm_nocollide"
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID: teamID, DisplayName: "NoCollide",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID, UserID: aliceID, Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withTx1(server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID, UserID: bobID, Role: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.resolveTeamShardCollisions(ctx, teamID, bobID)

	// Verify: no changes — both shards stay the same.
	aliceShard, _ := withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, aliceBoxID)
	bobShard, _ := withRxRes1(server, ctx, (*exedb.Queries).GetBoxIPShard, bobBoxID)
	if aliceShard != 1 {
		t.Errorf("alice shard changed: got %d, want 1", aliceShard)
	}
	if bobShard != 2 {
		t.Errorf("bob shard changed: got %d, want 2", bobShard)
	}
}

func TestParseTeamID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"tm_abc123", "tm_abc123", false},
		{"abc123", "tm_abc123", false},
		{"tm_IGM6MO7UZM7DX", "tm_IGM6MO7UZM7DX", false},
		{"tm_LQIZNYARG2SJ5", "tm_LQIZNYARG2SJ5", false},
		{"tm_mixed_Case_123", "tm_mixed_Case_123", false},
		{"tm_", "", true},
		{"", "", true},
		{"tm_has spaces", "", true},
		{"tm_has-dashes", "", true},
	}
	for _, tt := range tests {
		got, err := parseTeamID(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseTeamID(%q): err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseTeamID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
