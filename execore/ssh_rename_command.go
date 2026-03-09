package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"exe.dev/boxname"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
)

func (ss *SSHServer) handleRenameCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: rename <oldname> <newname>")
	}

	oldName := cc.Args[0]
	newName := cc.Args[1]

	CommandLogAddAttr(ctx, slog.String("vm_name", oldName))
	CommandLogAddAttr(ctx, slog.String("old_vm_name", oldName))
	CommandLogAddAttr(ctx, slog.String("new_vm_name", newName))

	// Check if renaming to the same name
	if oldName == newName {
		cc.Write("%s is already named %s\r\n", oldName, newName)
		return nil
	}

	// Validate new name
	if err := boxname.Valid(newName); err != nil {
		return cc.Errorf("invalid new name: %v", err)
	}

	// Check if the box exists and user has access (owner or team owner)
	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, oldName)
	if err != nil {
		return cc.Errorf("vm %q not found", oldName)
	}

	CommandLogAddAttr(ctx, slog.String("vm_owner_user_id", box.CreatedByUserID))
	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))

	// Check if new name already exists (globally)
	exists, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithNameExists, newName)
	if err != nil {
		return cc.Errorf("failed to check name availability: %v", err)
	}
	if exists != 0 {
		return cc.Errorf("name %q already exists", newName)
	}

	if box.ContainerID == nil {
		return cc.Errorf("vm %v not found", box.ContainerID)
	}

	exeletClient := ss.server.getExeletClient(box.Ctrhost)

	if exeletClient == nil {
		return cc.Errorf("internal error")
	}

	instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
		ID: *box.ContainerID,
	})

	if instanceResp == nil || err != nil || instanceResp.Instance.State != api.VMState_RUNNING {
		return cc.Errorf("vm rename not supported when vm is not running!")
	}

	// Update the box name in the database
	slog.InfoContext(ctx, "rename: starting DB update",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"user_id", cc.User.ID)
	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxNameByID, exedb.UpdateBoxNameByIDParams{
		Name: newName,
		ID:   box.ID,
	})
	if err != nil {
		slog.ErrorContext(ctx, "rename: DB update failed",
			"box_id", box.ID,
			"old_name", oldName,
			"new_name", newName,
			"error", err)
		return cc.Errorf("failed to rename vm: %v", err)
	}
	slog.InfoContext(ctx, "rename: DB update complete",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"rollback_sql", fmt.Sprintf("UPDATE boxes SET name = '%s' WHERE id = %d", oldName, box.ID))

	// Update the instance name on the exelet so the metadata service returns the new name
	slog.InfoContext(ctx, "rename: updating exelet instance config",
		"box_id", box.ID,
		"container_id", *box.ContainerID,
		"new_name", newName)
	_, err = exeletClient.client.RenameInstance(ctx, &api.RenameInstanceRequest{
		ID:   *box.ContainerID,
		Name: newName,
	})
	if err != nil {
		slog.ErrorContext(ctx, "rename: failed to update exelet instance config",
			"box_id", box.ID,
			"container_id", *box.ContainerID,
			"new_name", newName,
			"error", err)
		// Continue despite error - the DB rename succeeded, and the metadata service
		// will return the old name until the VM is restarted, which is not ideal but
		// not critical enough to roll back the entire rename.
	} else {
		slog.InfoContext(ctx, "rename: exelet instance config updated",
			"box_id", box.ID,
			"container_id", *box.ContainerID,
			"new_name", newName)
	}

	// Get the IP shard for DNS record update (need this early to create new DNS record)
	ipShard, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIPShardByBoxName, newName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.ErrorContext(ctx, "rename: failed to get IP shard for DNS - manual DNS check required",
			"box_id", box.ID,
			"old_name", oldName,
			"new_name", newName,
			"error", err)
	}

	// Invalidate all auth cookies for the old box name to prevent cookie hijacking.
	// When a box is renamed, any cookies issued for the old name's domain (e.g., oldname.exe.dev)
	// should be invalidated so that if someone else creates a box with the old name, they
	// cannot use any lingering cookies from the original owner.
	for _, oldDomain := range []string{
		ss.server.env.BoxSub(oldName),        // oldname.exe.cloud
		ss.server.env.BoxXtermSub(oldName),   // oldname.xterm.exe.cloud
		ss.server.env.BoxShelleySub(oldName), // oldname.shelley.exe.cloud
	} {
		if err := withTx1(ss.server, ctx, (*exedb.Queries).DeleteAuthCookiesByDomain, oldDomain); err != nil {
			slog.ErrorContext(ctx, "rename: failed to invalidate auth cookies for old box name",
				"box_id", box.ID,
				"old_name", oldName,
				"old_domain", oldDomain,
				"error", err)
		}
	}

	proxyChangeRenamedBox(oldName, newName)

	// Update hostname inside the running VM
	// Re-fetch the box with the new name for SSH connection
	slog.InfoContext(ctx, "rename: updating hostname inside VM",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName)
	updatedBox, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, newName)
	if err == nil {
		// Update /etc/hostname and /etc/hosts
		// Use busybox shell and sed to replace old hostname with new hostname
		// We use /exe.dev/bin/sh (busybox) to ensure sed/hostname are available even on minimal images
		// Safety: boxname.Valid rejects shell metacharacters, so Sprintf is safe here.
		hostnameCmd := fmt.Sprintf(
			"sudo /exe.dev/bin/sh -c 'sed -i \"s/\\b%s\\b/%s/g\" /etc/hostname /etc/hosts 2>/dev/null; hostname %s 2>/dev/null'",
			oldName, newName, newName,
		)
		if _, err := runCommandOnBox(ctx, ss.server.sshPool, updatedBox, hostnameCmd); err != nil {
			slog.ErrorContext(ctx, "rename: failed to update hostname inside VM",
				"box_id", box.ID,
				"old_name", oldName,
				"new_name", newName,
				"error", err)
		} else {
			slog.InfoContext(ctx, "rename: hostname update inside VM complete",
				"box_id", box.ID,
				"new_name", newName)
		}

		// Update /exe.dev/shelley.json with the new box name
		shelleyConf, err := ss.makeShelleyConfig(newName)
		if err != nil {
			slog.ErrorContext(ctx, "rename: failed to generate shelley.json",
				"box_id", box.ID,
				"new_name", newName,
				"error", err)
		} else {
			shelleyCmd := fmt.Sprintf("sudo /exe.dev/bin/sh -c 'cat > /exe.dev/shelley.json << \"SHELLEY_EOF\"\n%s\nSHELLEY_EOF'", shelleyConf)
			if _, err := runCommandOnBox(ctx, ss.server.sshPool, updatedBox, shelleyCmd); err != nil {
				slog.ErrorContext(ctx, "rename: failed to update shelley.json inside VM",
					"box_id", box.ID,
					"new_name", newName,
					"error", err)
			} else {
				slog.InfoContext(ctx, "rename: shelley.json update inside VM complete",
					"box_id", box.ID,
					"new_name", newName)
			}
		}
	} else {
		slog.ErrorContext(ctx, "rename: failed to re-fetch box for hostname update",
			"box_id", box.ID,
			"new_name", newName,
			"error", err)
	}

	slog.InfoContext(ctx, "rename: completed successfully",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"user_id", cc.User.ID,
		"ip_shard", ipShard)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{
			"old_name": oldName,
			"new_name": newName,
			"status":   "renamed",
		})
		return nil
	}
	cc.Write("Renamed %s to %s\r\n", oldName, newName)
	return nil
}
