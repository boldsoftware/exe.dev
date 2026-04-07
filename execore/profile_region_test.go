package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"exe.dev/exedb"
	"exe.dev/region"
	"exe.dev/sqlite"
)


// doProfileRegionRequest makes a POST /api/profile/region request using a cookie.
func doProfileRegionRequest(t *testing.T, s *Server, cookie, regionCode string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"region": regionCode})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/profile/region", s.httpPort()), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/profile/region: %v", err)
	}
	return resp
}

func doProfileRequest(t *testing.T, s *Server, cookie string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/profile", s.httpPort()), nil)
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/profile: %v", err)
	}
	return resp
}

func TestAPIProfileRegionUpdate(t *testing.T) {
	s := newTestServer(t)
	cookie := createTestUserWithCookie(t, s, "region-test@example.com")

	// Set user's region to fra directly so they're eligible for fra.
	var userID string
	s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "region-test@example.com").Scan(&userID)
	})
	if userID == "" {
		t.Fatal("user not found")
	}
	if err := withTx1(s, context.Background(), (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		UserID: userID,
		Region: "fra",
	}); err != nil {
		t.Fatalf("set region: %v", err)
	}

	t.Run("valid region change to lax", func(t *testing.T) {
		resp := doProfileRegionRequest(t, s, cookie, "lax")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result["region"] != "lax" {
			t.Errorf("region = %q, want %q", result["region"], "lax")
		}
		if result["regionDisplay"] == "" {
			t.Error("regionDisplay should not be empty")
		}
	})

	t.Run("unavailable region rejected", func(t *testing.T) {
		// User is now on lax; tyo requires user match and they're not on tyo.
		resp := doProfileRegionRequest(t, s, cookie, "tyo")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("unknown region rejected", func(t *testing.T) {
		resp := doProfileRegionRequest(t, s, cookie, "mars")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("unauthenticated request rejected", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"region": "lax"})
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/profile/region", s.httpPort()), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})
}

func TestAPIProfileIncludesRegionFields(t *testing.T) {
	s := newTestServer(t)
	cookie := createTestUserWithCookie(t, s, "profile-region-fields@example.com")

	resp := doProfileRequest(t, s, cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var profile jsonProfileData
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// availableRegions must be non-empty and all active.
	if len(profile.AvailableRegions) == 0 {
		t.Error("availableRegions should not be empty")
	}
	for _, r := range profile.AvailableRegions {
		reg, err := region.ByCode(r.Code)
		if err != nil {
			t.Errorf("availableRegions contains unknown code %q", r.Code)
			continue
		}
		if !reg.Active {
			t.Errorf("availableRegions contains inactive region %q", r.Code)
		}
		if r.Display == "" {
			t.Errorf("availableRegions[%q].display is empty", r.Code)
		}
	}

}
