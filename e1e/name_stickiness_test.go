package e1e

import "testing"

// TestNameStickiness verifies that after a user deletes a VM, its name
// is reserved for the original owner for 24 hours. Another user who
// tries to create a VM with the same name gets an error, while the
// original owner can reclaim it.
func TestNameStickiness(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2) // user1 creates, deletes, then reclaims; user2 attempts one
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register two independent users.
	user1PTY, _, user1Key, _ := registerForExeDevWithEmail(t, "sticky-owner@name-stickiness.example")
	user2PTY, _, user2Key, _ := registerForExeDevWithEmail(t, "sticky-other@name-stickiness.example")

	// User 1 creates a VM.
	box := newBox(t, user1PTY)
	user1PTY.Disconnect()
	waitForSSH(t, box, user1Key)

	// User 1 deletes the VM.
	user1PTY = sshToExeDev(t, user1Key)
	user1PTY.deleteBox(box)
	user1PTY.Disconnect()
	waitForBoxGone(t, user1Key, box)

	// User 2 tries to create a VM with the same name — should fail.
	user2PTY.SendLine("new --name=" + box)
	user2PTY.Want("not available")
	user2PTY.WantPrompt()
	user2PTY.Disconnect()

	// User 2 also cannot rename an existing VM to the sticky name.
	user2PTY = sshToExeDev(t, user2Key)
	user2Box := newBox(t, user2PTY)
	user2PTY.Disconnect()
	waitForSSH(t, user2Box, user2Key)

	user2PTY = sshToExeDev(t, user2Key)
	user2PTY.SendLine("rename " + user2Box + " " + box)
	user2PTY.Want("not available")
	user2PTY.WantPrompt()
	user2PTY.Disconnect()

	// User 1 reclaims the name — should succeed.
	user1PTY = sshToExeDev(t, user1Key)
	user1PTY.SendLine("new --name=" + box)
	user1PTY.Want("Creating")
	user1PTY.Want(box)
	user1PTY.WantPrompt()
	user1PTY.Disconnect()
	waitForSSH(t, box, user1Key)

	// Cleanup.
	cleanupBox(t, user1Key, box)
	cleanupBox(t, user2Key, user2Box)
}
