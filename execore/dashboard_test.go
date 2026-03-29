package execore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

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
	Boxes []jsonBoxInfo `json:"boxes"`
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
