package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// handleChownCommand reassigns a VM to a different user account.
//
// Updates:
//   - boxes.created_by_user_id
//   - box_ip_shard.user_id (reassigning shard if it collides with the new owner's existing shards)
//   - deletes any outgoing shares (individual, team, share-links, pending) since they were created by the prior owner
//   - clears released_box_name entries pointing to this box
//   - invalidates auth cookies for the VM's subdomains
//   - reparents the exelet cgroup (SetInstanceGroup)
func (ss *SSHServer) handleChownCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: sudo-exe chown <vmname> <new-user-email-or-id>")
	}
	vmName := cc.Args[0]
	target := cc.Args[1]

	CommandLogAddAttr(ctx, slog.String("vm_name", vmName))

	// Resolve the new owner.
	newOwner, err := ss.resolveUserForCredits(ctx, target)
	if err != nil {
		return cc.Errorf("user not found: %s", target)
	}

	// Find the box by name. sudo-exe chown is allowed for any VM.
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxNamed, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cc.Errorf("vm %q not found", vmName)
		}
		cc.WriteInternalError(ctx, "sudo-exe chown", err)
		return nil
	}

	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))
	CommandLogAddAttr(ctx, slog.String("old_owner", box.CreatedByUserID))
	CommandLogAddAttr(ctx, slog.String("new_owner", newOwner.UserID))

	if box.CreatedByUserID == newOwner.UserID {
		return cc.Errorf("vm %q is already owned by %s", vmName, newOwner.Email)
	}
	if box.ContainerID == nil {
		return cc.Errorf("can't chown %q while it is still being created", vmName)
	}

	// Perform the DB rewiring in a single transaction. We also snapshot the existing shares
	// inside the transaction so the post-commit proxy-cache invalidation is consistent with
	// the deletes.
	var newShard int64
	var individualShares []exedb.GetBoxSharesByBoxIDRow
	var teamShares []exedb.GetBoxTeamSharesByBoxIDRow
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		// Optimistic lock: update succeeds only if the current owner still matches
		// what we read outside the transaction. Guards against concurrent chown/transfer.
		rows, err := queries.UpdateBoxOwnerIfCurrent(ctx, exedb.UpdateBoxOwnerIfCurrentParams{
			CreatedByUserID:   newOwner.UserID,
			ID:                box.ID,
			CreatedByUserID_2: box.CreatedByUserID,
		})
		if err != nil {
			return fmt.Errorf("updating box owner: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("box ownership changed concurrently; retry the chown")
		}

		// Snapshot shares *inside* the tx so invalidations match what we deleted.
		individualShares, err = queries.GetBoxSharesByBoxID(ctx, int64(box.ID))
		if err != nil {
			return fmt.Errorf("reading individual shares: %w", err)
		}
		teamShares, err = queries.GetBoxTeamSharesByBoxID(ctx, int64(box.ID))
		if err != nil {
			return fmt.Errorf("reading team shares: %w", err)
		}

		// Determine current shard and whether it collides with new owner's existing shards.
		curShard, err := queries.GetBoxIPShard(ctx, box.ID)
		if err != nil {
			return fmt.Errorf("loading current ip shard: %w", err)
		}

		// ListIPShardsForUser returns shards for all of new owner's boxes EXCEPT the one we're
		// about to reassign (because box_ip_shard.user_id for this box is still the old owner).
		others, err := queries.ListIPShardsForUser(ctx, newOwner.UserID)
		if err != nil {
			return fmt.Errorf("listing new owner ip shards: %w", err)
		}
		used := make([]bool, ss.server.env.NumShards+1)
		for _, s := range others {
			if ss.server.env.ShardIsValid(int(s)) {
				used[int(s)] = true
			}
		}

		newShard = curShard
		if ss.server.env.ShardIsValid(int(curShard)) && used[int(curShard)] {
			newShard = 0
			for cand := 1; cand <= ss.server.env.NumShards; cand++ {
				if !used[cand] {
					newShard = int64(cand)
					break
				}
			}
			if newShard == 0 {
				// All shards in use. Reuse shard 1; HTTP routes by Host header and SSH by
				// `vm+name@` so functionality is intact — only shard-IP-based SSH is ambiguous.
				newShard = 1
				slog.WarnContext(ctx, "chown: all shards exhausted, reusing shard 1",
					"box_id", box.ID, "user_id", newOwner.UserID)
			}
		}

		// The PK of box_ip_shard is (box_id, user_id); user_id is changing. Do it as
		// delete+insert to avoid partial-update edge cases.
		if err := queries.DeleteBoxIPShard(ctx, box.ID); err != nil {
			return fmt.Errorf("deleting old box_ip_shard row: %w", err)
		}
		if err := queries.InsertBoxIPShard(ctx, exedb.InsertBoxIPShardParams{
			BoxID:   box.ID,
			UserID:  newOwner.UserID,
			IPShard: newShard,
		}); err != nil {
			return fmt.Errorf("inserting new box_ip_shard row: %w", err)
		}

		// Clear shares — they were created by the old owner and no longer meaningful.
		// Bulk-delete by box_id so we can't miss a row added after the snapshot.
		if err := queries.DeleteBoxSharesByBox(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting individual shares: %w", err)
		}
		if err := queries.DeleteBoxTeamSharesByBoxID(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting team shares: %w", err)
		}
		if err := queries.DeletePendingBoxSharesByBox(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting pending shares: %w", err)
		}
		if err := queries.DeleteBoxShareLinksByBox(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting box share links: %w", err)
		}

		// Drop any sticky name reservations pointing at this box.
		if err := queries.DeleteReleasedBoxNamesByBoxID(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting released box names: %w", err)
		}

		// Invalidate auth cookies for this VM's subdomains. Inside the tx so we never
		// end up with owner rewired but old cookies still valid.
		for _, domain := range []string{
			ss.server.env.BoxSub(vmName),
			ss.server.env.BoxXtermSub(vmName),
			ss.server.env.BoxShelleySub(vmName),
		} {
			if err := queries.DeleteAuthCookiesByDomain(ctx, domain); err != nil {
				return fmt.Errorf("deleting auth cookies for %s: %w", domain, err)
			}
		}

		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "sudo-exe chown: db update failed",
			"box_id", box.ID, "old_owner", box.CreatedByUserID,
			"new_owner", newOwner.UserID, "error", err)
		return cc.Errorf("failed to chown vm: %v", err)
	}

	// Notify proxy to drop cached routing/share data.
	proxyChangeDeletedBox(vmName)
	for _, share := range individualShares {
		proxyChangeDeletedBoxShare(vmName, share.SharedWithUserID)
	}
	for _, ts := range teamShares {
		proxyChangeDeletedBoxShareTeam(ts.TeamID, int(ts.BoxID), vmName)
	}

	// Reparent the exelet cgroup. The resource manager will move the VM into the new
	// account slice on its next poll (~30s).
	exeletClient := ss.server.getExeletClient(box.Ctrhost)
	if exeletClient != nil && box.ContainerID != nil {
		if _, err := exeletClient.client.SetInstanceGroup(ctx, &api.SetInstanceGroupRequest{
			ID:      *box.ContainerID,
			GroupID: newOwner.UserID,
		}); err != nil {
			slog.ErrorContext(ctx, "chown: failed to set exelet group",
				"box_id", box.ID, "container_id", *box.ContainerID,
				"new_owner", newOwner.UserID, "error", err)
			cc.Writeln("WARNING: DB updated but exelet cgroup reparent failed: %v", err)
		}
	}

	slog.InfoContext(ctx, "sudo-exe chown: completed",
		"box_id", box.ID, "vm_name", vmName,
		"old_owner", box.CreatedByUserID, "new_owner", newOwner.UserID,
		"new_shard", newShard, "by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name":   vmName,
			"old_owner": box.CreatedByUserID,
			"new_owner": newOwner.UserID,
			"new_shard": newShard,
			"status":    "chowned",
		})
		return nil
	}
	cc.Writeln("Chowned %s from %s to %s (%s)", vmName, box.CreatedByUserID, newOwner.UserID, newOwner.Email)
	return nil
}
