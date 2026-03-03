package execore

import (
	"testing"

	"exe.dev/exeweb"
)

// TestLoopbackProxyData tests that the gRPC loopback implementation
// of ProxyData produces the same results as the direct in-process
// implementation for all exercisable methods.
func TestLoopbackProxyData(t *testing.T) {
	s := newTestServer(t)
	ctx := t.Context()

	inProcess := &proxyData{s: s}
	loopback := s.loopbackProxyData

	// Seed a user.
	userID := createTestUser(t, s, "loopback@example.com")

	// Seed a box.
	s.createTestBox(t, userID, "ctrhost1", "loopbox", "ctr1", "exeuntu")

	// Seed a cookie.
	cookieValue, err := s.createAuthCookie(ctx, userID, s.env.WebHost)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("BoxInfo_exists", func(t *testing.T) {
		ipBox, ipOK, ipErr := inProcess.BoxInfo(ctx, "loopbox")
		lbBox, lbOK, lbErr := loopback.BoxInfo(ctx, "loopbox")

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
		if ipBox.Name != lbBox.Name {
			t.Errorf("Name: in-process=%q, loopback=%q", ipBox.Name, lbBox.Name)
		}
		if ipBox.ID != lbBox.ID {
			t.Errorf("ID: in-process=%d, loopback=%d", ipBox.ID, lbBox.ID)
		}
		if ipBox.CreatedByUserID != lbBox.CreatedByUserID {
			t.Errorf("CreatedByUserID: in-process=%q, loopback=%q", ipBox.CreatedByUserID, lbBox.CreatedByUserID)
		}
		if ipBox.Status != lbBox.Status {
			t.Errorf("Status: in-process=%q, loopback=%q", ipBox.Status, lbBox.Status)
		}
		if ipBox.Image != lbBox.Image {
			t.Errorf("Image: in-process=%q, loopback=%q", ipBox.Image, lbBox.Image)
		}
		if ipBox.BoxRoute != lbBox.BoxRoute {
			t.Errorf("BoxRoute: in-process=%+v, loopback=%+v", ipBox.BoxRoute, lbBox.BoxRoute)
		}
	})

	t.Run("BoxInfo_not_found", func(t *testing.T) {
		_, ipOK, ipErr := inProcess.BoxInfo(ctx, "nonexistent")
		_, lbOK, lbErr := loopback.BoxInfo(ctx, "nonexistent")

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("CookieInfo_exists", func(t *testing.T) {
		ipCD, ipOK, ipErr := inProcess.CookieInfo(ctx, cookieValue, s.env.WebHost)
		lbCD, lbOK, lbErr := loopback.CookieInfo(ctx, cookieValue, s.env.WebHost)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
		if ipCD.UserID != lbCD.UserID {
			t.Errorf("UserID: in-process=%q, loopback=%q", ipCD.UserID, lbCD.UserID)
		}
		if ipCD.Domain != lbCD.Domain {
			t.Errorf("Domain: in-process=%q, loopback=%q", ipCD.Domain, lbCD.Domain)
		}
	})

	t.Run("CookieInfo_not_found", func(t *testing.T) {
		_, ipOK, ipErr := inProcess.CookieInfo(ctx, "nonexistent", s.env.WebHost)
		_, lbOK, lbErr := loopback.CookieInfo(ctx, "nonexistent", s.env.WebHost)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("UserInfo_exists", func(t *testing.T) {
		ipUD, ipOK, ipErr := inProcess.UserInfo(ctx, userID)
		lbUD, lbOK, lbErr := loopback.UserInfo(ctx, userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
		if ipUD.UserID != lbUD.UserID {
			t.Errorf("UserID: in-process=%q, loopback=%q", ipUD.UserID, lbUD.UserID)
		}
		if ipUD.Email != lbUD.Email {
			t.Errorf("Email: in-process=%q, loopback=%q", ipUD.Email, lbUD.Email)
		}
	})

	t.Run("UserInfo_not_found", func(t *testing.T) {
		_, ipOK, ipErr := inProcess.UserInfo(ctx, "nonexistent")
		_, lbOK, lbErr := loopback.UserInfo(ctx, "nonexistent")

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Fatalf("exists: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("IsUserLockedOut", func(t *testing.T) {
		ipLocked, ipErr := inProcess.IsUserLockedOut(ctx, userID)
		lbLocked, lbErr := loopback.IsUserLockedOut(ctx, userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipLocked != lbLocked {
			t.Errorf("locked: in-process=%v, loopback=%v", ipLocked, lbLocked)
		}
	})

	t.Run("UserHasExeSudo", func(t *testing.T) {
		ipSudo, ipErr := inProcess.UserHasExeSudo(ctx, userID)
		lbSudo, lbErr := loopback.UserHasExeSudo(ctx, userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipSudo != lbSudo {
			t.Errorf("sudo: in-process=%v, loopback=%v", ipSudo, lbSudo)
		}
	})

	t.Run("CreateAndDeleteAuthCookie", func(t *testing.T) {
		lbCookie, err := loopback.CreateAuthCookie(ctx, userID, s.env.WebHost)
		if err != nil {
			t.Fatal(err)
		}
		if lbCookie == "" {
			t.Fatal("loopback CreateAuthCookie returned empty cookie")
		}

		// Verify via in-process path that the cookie exists.
		_, ok, err := inProcess.CookieInfo(ctx, lbCookie, s.env.WebHost)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("cookie created via loopback not found via in-process")
		}

		// Delete via loopback.
		if err := loopback.DeleteAuthCookie(ctx, lbCookie); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("UsedCookie", func(t *testing.T) {
		// Just verify it doesn't panic or error.
		loopback.UsedCookie(ctx, cookieValue)
	})

	t.Run("HasUserAccessToBox_no_share", func(t *testing.T) {
		ipOK, ipErr := inProcess.HasUserAccessToBox(ctx, 1, "loopbox", userID)
		lbOK, lbErr := loopback.HasUserAccessToBox(ctx, 1, "loopbox", userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Errorf("hasAccess: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("IsBoxSharedWithUserTeam_no_team", func(t *testing.T) {
		ipOK, ipErr := inProcess.IsBoxSharedWithUserTeam(ctx, 1, "loopbox", userID)
		lbOK, lbErr := loopback.IsBoxSharedWithUserTeam(ctx, 1, "loopbox", userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Errorf("isTeamShared: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("IsBoxShelleySharedWithTeamMember", func(t *testing.T) {
		ipOK, ipErr := inProcess.IsBoxShelleySharedWithTeamMember(ctx, 1, "loopbox", userID)
		lbOK, lbErr := loopback.IsBoxShelleySharedWithTeamMember(ctx, 1, "loopbox", userID)

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Errorf("isShelleyShared: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("CheckShareLink_no_link", func(t *testing.T) {
		ipOK, ipErr := inProcess.CheckShareLink(ctx, 1, "loopbox", userID, "")
		lbOK, lbErr := loopback.CheckShareLink(ctx, 1, "loopbox", userID, "")

		if ipErr != nil || lbErr != nil {
			t.Fatalf("errors: in-process=%v, loopback=%v", ipErr, lbErr)
		}
		if ipOK != lbOK {
			t.Errorf("valid: in-process=%v, loopback=%v", ipOK, lbOK)
		}
	})

	t.Run("ValidateMagicSecret_invalid", func(t *testing.T) {
		_, _, _, ipErr := inProcess.ValidateMagicSecret(ctx, "invalid-secret")
		_, _, _, lbErr := loopback.ValidateMagicSecret(ctx, "invalid-secret")

		// Both should error.
		if ipErr == nil {
			t.Error("in-process: expected error for invalid secret")
		}
		if lbErr == nil {
			t.Error("loopback: expected error for invalid secret")
		}
	})

	t.Run("GetSSHKeyByFingerprint_not_found", func(t *testing.T) {
		_, _, ipErr := inProcess.GetSSHKeyByFingerprint(ctx, "nonexistent")
		_, _, lbErr := loopback.GetSSHKeyByFingerprint(ctx, "nonexistent")

		// Both should error.
		if ipErr == nil {
			t.Error("in-process: expected error for nonexistent key")
		}
		if lbErr == nil {
			t.Error("loopback: expected error for nonexistent key")
		}
	})

	t.Run("HLLNoteEvents", func(t *testing.T) {
		// Just verify it doesn't panic or error.
		loopback.HLLNoteEvents(ctx, userID, []string{"test-event"})
	})

	t.Run("AppTokenValidator", func(t *testing.T) {
		atv, ok := loopback.(exeweb.AppTokenValidator)
		if !ok {
			t.Fatal("loopback does not implement AppTokenValidator")
		}
		// With no tokens configured, validation should fail.
		_, err := atv.ValidateAppToken(ctx, "nonexistent")
		if err == nil {
			t.Error("expected error for invalid app token")
		}
	})
}
