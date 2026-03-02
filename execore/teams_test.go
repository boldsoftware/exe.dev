package execore

import (
	"context"
	"testing"

	"exe.dev/exedb"
)

func TestResolveTeamShardCollisions(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()

	// Create two users.
	aliceID := createTestUser(t, server, "alice@shard-test.example")
	bobID := createTestUser(t, server, "bob@shard-test.example")

	// Create a box for each user (noShard), then manually assign both to shard 1.
	aliceBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:  aliceID,
		ctrhost: "tcp://fake:9080",
		name:    "alice-vm",
		image:   "ubuntu:latest",
		noShard: true,
		region:  "pdx",
	})
	if err != nil {
		t.Fatal(err)
	}
	bobBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:  bobID,
		ctrhost: "tcp://fake:9080",
		name:    "bob-vm",
		image:   "ubuntu:latest",
		noShard: true,
		region:  "pdx",
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

	// Create team, add Alice as sudoer.
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
		Role:   "sudoer",
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
		userID:  aliceID,
		ctrhost: "tcp://fake:9080",
		name:    "alice-nc-vm",
		image:   "ubuntu:latest",
		noShard: true,
		region:  "pdx",
	})
	if err != nil {
		t.Fatal(err)
	}
	bobBoxID, err := server.preCreateBox(ctx, preCreateBoxOptions{
		userID:  bobID,
		ctrhost: "tcp://fake:9080",
		name:    "bob-nc-vm",
		image:   "ubuntu:latest",
		noShard: true,
		region:  "pdx",
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
		TeamID: teamID, UserID: aliceID, Role: "sudoer",
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
