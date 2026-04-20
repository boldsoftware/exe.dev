package execore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestAPIDashboardBatchShareData(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "dash-shares@example.com")

	// Look up the user.
	var userID string
	s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "dash-shares@example.com").Scan(&userID)
	})
	if userID == "" {
		t.Fatal("user not found")
	}

	// Insert two boxes.
	var box1ID, box2ID int64
	err := s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		var err error
		box1ID, err = q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "box-one", Status: "running",
			Image: "test-image", CreatedByUserID: userID, Region: "pdx",
		})
		if err != nil {
			return err
		}
		box2ID, err = q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "box-two", Status: "running",
			Image: "test-image", CreatedByUserID: userID, Region: "pdx",
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert boxes: %v", err)
	}

	// Add sharing data: pending share on box1, active share on box1, share link on box2.
	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		_, err := q.CreatePendingBoxShare(ctx, exedb.CreatePendingBoxShareParams{
			BoxID: box1ID, SharedWithEmail: "pending@example.com",
			SharedByUserID: userID,
		})
		if err != nil {
			return err
		}

		// Create another user for active share.
		err = q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: "other-user", Email: "active@example.com", Region: "pdx",
		})
		if err != nil {
			return err
		}
		_, err = q.CreateBoxShare(ctx, exedb.CreateBoxShareParams{
			BoxID: box1ID, SharedWithUserID: "other-user",
			SharedByUserID: userID,
		})
		if err != nil {
			return err
		}

		_, err = q.CreateBoxShareLink(ctx, exedb.CreateBoxShareLinkParams{
			BoxID: box2ID, ShareToken: "test-token-abc",
			CreatedByUserID: userID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create shares: %v", err)
	}

	// Call the dashboard API.
	req, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var dash DashboardData
	if err := json.NewDecoder(resp.Body).Decode(&dash); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Find our two boxes.
	var b1, b2 *jsonBoxInfo
	for i := range dash.Boxes {
		switch dash.Boxes[i].Name {
		case "box-one":
			b1 = &dash.Boxes[i]
		case "box-two":
			b2 = &dash.Boxes[i]
		}
	}
	if b1 == nil || b2 == nil {
		t.Fatalf("expected both boxes in response, got %d boxes", len(dash.Boxes))
	}

	// box-one: 1 pending + 1 active = 2 user shares, 0 link shares.
	if b1.SharedUserCount != 2 {
		t.Errorf("box-one SharedUserCount = %d, want 2", b1.SharedUserCount)
	}
	if b1.ShareLinkCount != 0 {
		t.Errorf("box-one ShareLinkCount = %d, want 0", b1.ShareLinkCount)
	}
	if b1.TotalShareCount != 2 {
		t.Errorf("box-one TotalShareCount = %d, want 2", b1.TotalShareCount)
	}
	wantEmails := map[string]bool{"pending@example.com": true, "active@example.com": true}
	gotEmails := map[string]bool{}
	for _, e := range b1.SharedEmails {
		gotEmails[e] = true
	}
	if len(gotEmails) != len(wantEmails) {
		t.Errorf("box-one SharedEmails = %v, want %v", b1.SharedEmails, wantEmails)
	}
	for e := range wantEmails {
		if !gotEmails[e] {
			t.Errorf("box-one SharedEmails missing %q", e)
		}
	}

	// box-two: 0 user shares, 1 link share.
	if b2.SharedUserCount != 0 {
		t.Errorf("box-two SharedUserCount = %d, want 0", b2.SharedUserCount)
	}
	if b2.ShareLinkCount != 1 {
		t.Errorf("box-two ShareLinkCount = %d, want 1", b2.ShareLinkCount)
	}
	if b2.TotalShareCount != 1 {
		t.Errorf("box-two TotalShareCount = %d, want 1", b2.TotalShareCount)
	}
	if len(b2.ShareLinks) != 1 {
		t.Errorf("box-two ShareLinks = %v, want 1 link", b2.ShareLinks)
	} else if b2.ShareLinks[0].Token != "test-token-abc" {
		t.Errorf("box-two ShareLinks[0].Token = %q, want %q", b2.ShareLinks[0].Token, "test-token-abc")
	}
	if len(b2.SharedEmails) != 0 {
		t.Errorf("box-two SharedEmails = %v, want empty", b2.SharedEmails)
	}
}

// DashboardData mirrors the JSON structure returned by /api/dashboard.
// We only include fields we care about for this test.
type DashboardData struct {
	Boxes           []jsonBoxInfo       `json:"boxes"`
	SharedBoxes     []jsonSharedBox     `json:"sharedBoxes"`
	TeamSharedBoxes []jsonTeamSharedBox `json:"teamSharedBoxes"`
	TeamBoxes       []jsonTeamBox       `json:"teamBoxes"`
	HasTeam         bool                `json:"hasTeam"`
}

func TestAPIDashboardTeamSharedBoxes(t *testing.T) {
	s := newTestServer(t)

	// Create two users: team admin and team member
	adminToken := createTestUserWithAppToken(t, s, "admin@example.com")
	memberToken := createTestUserWithAppToken(t, s, "member@example.com")

	// Look up user IDs
	var adminID, memberID string
	s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		if err := rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "admin@example.com").Scan(&adminID); err != nil {
			return err
		}
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "member@example.com").Scan(&memberID)
	})

	// Create a team
	teamID, err := s.EnableTeam(context.Background(), adminID, "TestTeam")
	if err != nil {
		t.Fatalf("EnableTeam: %v", err)
	}

	// Add member to team
	if err := s.addTeamMember(context.Background(), teamID, memberID, "user"); err != nil {
		t.Fatalf("addTeamMember: %v", err)
	}

	// Create a box owned by the member
	var memberBoxID int64
	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		var err error
		memberBoxID, err = q.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost: "test-host", Name: "member-vm", Status: "running",
			Image: "test-image", CreatedByUserID: memberID, Region: "pdx",
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert box: %v", err)
	}

	// Share the member's box with the team
	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertBoxTeamShare(ctx, exedb.InsertBoxTeamShareParams{
			BoxID:    memberBoxID,
			TeamID:   teamID,
			SharedBy: memberID,
		})
	})
	if err != nil {
		t.Fatalf("InsertBoxTeamShare: %v", err)
	}

	// Admin's dashboard should show the box in teamSharedBoxes
	req, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var dash DashboardData
	if err := json.NewDecoder(resp.Body).Decode(&dash); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !dash.HasTeam {
		t.Error("expected HasTeam=true for admin")
	}

	// The box should appear in teamSharedBoxes for the admin
	if len(dash.TeamSharedBoxes) != 1 {
		t.Fatalf("expected 1 teamSharedBox, got %d", len(dash.TeamSharedBoxes))
	}
	if dash.TeamSharedBoxes[0].Name != "member-vm" {
		t.Errorf("teamSharedBoxes[0].Name = %q, want %q", dash.TeamSharedBoxes[0].Name, "member-vm")
	}
	if dash.TeamSharedBoxes[0].OwnerEmail != "member@example.com" {
		t.Errorf("teamSharedBoxes[0].OwnerEmail = %q, want %q", dash.TeamSharedBoxes[0].OwnerEmail, "member@example.com")
	}

	// The box should NOT also appear in teamBoxes (admin can see team boxes,
	// but the team-shared box should be deduplicated)
	for _, tb := range dash.TeamBoxes {
		if tb.Name == "member-vm" {
			t.Error("member-vm should not appear in both teamSharedBoxes and teamBoxes")
		}
	}

	// Member's dashboard should also see the box in teamSharedBoxes (shared by themselves, but they're not the owner? No, they ARE the owner)
	// Actually, the member owns the box so it should appear in their own boxes, NOT in teamSharedBoxes
	req2, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req2.Header.Set("Authorization", "Bearer "+memberToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /api/dashboard (member): %v", err)
	}
	defer resp2.Body.Close()

	var dash2 DashboardData
	if err := json.NewDecoder(resp2.Body).Decode(&dash2); err != nil {
		t.Fatalf("decode response (member): %v", err)
	}

	if !dash2.HasTeam {
		t.Error("expected HasTeam=true for member")
	}

	// Member owns the box, so it should be in their boxes with isTeamShared=true
	found := false
	for _, b := range dash2.Boxes {
		if b.Name == "member-vm" {
			found = true
			if !b.IsTeamShared {
				t.Error("expected IsTeamShared=true on member's own box")
			}
		}
	}
	if !found {
		t.Error("member's box not found in their own boxes")
	}

	// Member should NOT see it in teamSharedBoxes (it's their own box)
	for _, tsb := range dash2.TeamSharedBoxes {
		if tsb.Name == "member-vm" {
			t.Error("member-vm should not appear in member's teamSharedBoxes (they own it)")
		}
	}
}

func TestAPIDashboardTrialBanner(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "trial-user@example.com")

	// Look up user ID and account.
	var userID string
	s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "trial-user@example.com").Scan(&userID)
	})
	if userID == "" {
		t.Fatal("user not found")
	}

	// Fetch dashboard before granting trial — no trial field.
	req, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var dashBefore struct {
		Trial *jsonTrialInfo `json:"trial"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dashBefore); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dashBefore.Trial != nil {
		t.Fatalf("expected no trial info before granting trial, got %+v", dashBefore.Trial)
	}

	// Grant trial to user.
	now := time.Now()
	trialEnd := now.AddDate(0, 0, 14)
	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		acct, err := q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      acct.ID,
			PlanID:         "trial:monthly:20260106",
			At:             now,
			TrialExpiresAt: &trialEnd,
			ChangedBy:      "test",
		})
	})
	if err != nil {
		t.Fatalf("grant trial: %v", err)
	}

	// Fetch dashboard after granting trial — should have trial info.
	req2, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req2.Header.Set("Authorization", "Bearer "+appToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp2.Body.Close()
	var dashAfter struct {
		Trial *jsonTrialInfo `json:"trial"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&dashAfter); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dashAfter.Trial == nil {
		t.Fatal("expected trial info after granting trial, got nil")
	}
	if dashAfter.Trial.DaysLeft < 13 || dashAfter.Trial.DaysLeft > 15 {
		t.Errorf("expected ~14 daysLeft, got %d", dashAfter.Trial.DaysLeft)
	}
	if dashAfter.Trial.Expired {
		t.Error("expected active trial, got expired")
	}

	// Expire the trial.
	past := now.Add(-24 * time.Hour)
	err = s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		acct, err := q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      acct.ID,
			PlanID:         "trial:monthly:20260106",
			At:             now,
			TrialExpiresAt: &past,
			ChangedBy:      "test",
		})
	})
	if err != nil {
		t.Fatalf("expire trial: %v", err)
	}

	req3, _ := http.NewRequest("GET", s.httpURL()+"/api/dashboard", nil)
	req3.Header.Set("Authorization", "Bearer "+appToken)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp3.Body.Close()
	var dashExpired struct {
		Trial *jsonTrialInfo `json:"trial"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&dashExpired); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dashExpired.Trial == nil {
		t.Fatal("expected trial info for expired trial, got nil")
	}
	if !dashExpired.Trial.Expired {
		t.Error("expected expired=true for expired trial")
	}
}

func TestAPIProfileTrialBanner(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "profile-trial@example.com")

	var userID string
	s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "profile-trial@example.com").Scan(&userID)
	})
	if userID == "" {
		t.Fatal("user not found")
	}

	fetchProfile := func() *jsonTrialInfo {
		t.Helper()
		req, _ := http.NewRequest("GET", s.httpURL()+"/api/profile", nil)
		req.Header.Set("Authorization", "Bearer "+appToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/profile: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var profile struct {
			Trial *jsonTrialInfo `json:"trial"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return profile.Trial
	}

	// No trial before granting.
	if trial := fetchProfile(); trial != nil {
		t.Fatalf("expected no trial before granting, got %+v", trial)
	}

	// Grant trial.
	now := time.Now()
	trialEnd := now.AddDate(0, 0, 7)
	if err := s.withTx(context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		acct, err := q.GetAccountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		return q.ReplaceAccountPlan(ctx, exedb.ReplaceAccountPlanParams{
			AccountID:      acct.ID,
			PlanID:         "trial:monthly:20260106",
			At:             now,
			TrialExpiresAt: &trialEnd,
			ChangedBy:      "test",
		})
	}); err != nil {
		t.Fatalf("grant trial: %v", err)
	}

	// Should have trial info now.
	trial := fetchProfile()
	if trial == nil {
		t.Fatal("expected trial info after granting, got nil")
	}
	if trial.DaysLeft < 6 || trial.DaysLeft > 8 {
		t.Errorf("expected ~7 daysLeft, got %d", trial.DaysLeft)
	}
}

func TestRegionDisplay(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"pdx", "Oregon, USA"},
		{"fra", "Frankfurt, Germany"},
		{"", ""},
		{"bogus", ""},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := regionDisplay(tt.code)
			if got != tt.want {
				t.Errorf("regionDisplay(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}
