package execore

import (
	"context"
	"log/slog"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

func (ss *SSHServer) handleLockCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: lock <vmname> [reason]")
	}

	if len(cc.Args) > 2 {
		return cc.Errorf("usage: lock <vmname> [reason]\n(use quotes around multi-word reasons: lock myvm \"ask pandora before unlocking\")")
	}

	vmName := ss.normalizeBoxName(cc.Args[0])
	reason := "locked"
	if len(cc.Args) > 1 {
		reason = cc.Args[1]
	}

	CommandLogAddAttr(ctx, slog.String("vm_name", vmName))

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}

	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))

	if box.LockReason != nil {
		return cc.Errorf("vm %q is already locked: %q", vmName, *box.LockReason)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetBoxLockReason, exedb.SetBoxLockReasonParams{
		LockReason: &reason,
		ID:         box.ID,
	})
	if err != nil {
		return cc.Errorf("failed to lock vm: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name":     vmName,
			"lock_reason": reason,
		})
		return nil
	}

	cc.Writeln("Locked %q: %q", vmName, reason)
	return nil
}

func (ss *SSHServer) handleUnlockCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.IsSSHExec() {
		return cc.Errorf("unlock is only available in the interactive repl")
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: unlock <vmname>")
	}

	vmName := ss.normalizeBoxName(cc.Args[0])

	CommandLogAddAttr(ctx, slog.String("vm_name", vmName))

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}

	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))

	if box.LockReason == nil {
		return cc.Errorf("vm %q is not locked", vmName)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetBoxLockReason, exedb.SetBoxLockReasonParams{
		LockReason: nil,
		ID:         box.ID,
	})
	if err != nil {
		return cc.Errorf("failed to unlock vm: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name": vmName,
			"locked":  false,
		})
		return nil
	}

	cc.Writeln("Unlocked %q", vmName)
	return nil
}
