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
	"exe.dev/sqlite"
)

func TestPushTokenRegister(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "push@example.com")

	body, _ := json.Marshal(pushTokenRequest{Token: "aabbccdd", Platform: "apns"})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", s.httpPort()), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result pushTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	// Verify in DB.
	var userID string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "push@example.com").Scan(&userID)
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetPushTokensByUserID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 push token, got %d", len(tokens))
	}
	if tokens[0].Token != "aabbccdd" || tokens[0].Platform != "apns" {
		t.Fatalf("unexpected token: %+v", tokens[0])
	}
}

func TestPushTokenRegister_Upsert(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "upsert@example.com")
	port := s.httpPort()

	// Register same token twice — should not create duplicates.
	for i := 0; i < 2; i++ {
		body, _ := json.Marshal(pushTokenRequest{Token: "deadbeef", Platform: "apns"})
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", port), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+appToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("attempt %d: expected 200, got %d", i, resp.StatusCode)
		}
	}

	var userID string
	err := s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "upsert@example.com").Scan(&userID)
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetPushTokensByUserID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 push token after upsert, got %d", len(tokens))
	}
}

func TestPushTokenRegister_RejectNonAppToken(t *testing.T) {
	s := newTestServer(t)
	port := s.httpPort()

	body, _ := json.Marshal(pushTokenRequest{Token: "aabbccdd", Platform: "apns"})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", port), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer not-an-app-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestPushTokenRegister_RejectNoAuth(t *testing.T) {
	s := newTestServer(t)
	port := s.httpPort()

	body, _ := json.Marshal(pushTokenRequest{Token: "aabbccdd", Platform: "apns"})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", port), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestPushTokenRegister_RejectBadHex(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "badhex@example.com")

	body, _ := json.Marshal(pushTokenRequest{Token: "not-valid-hex!", Platform: "apns"})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", s.httpPort()), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPushTokenDelete(t *testing.T) {
	s := newTestServer(t)
	appToken := createTestUserWithAppToken(t, s, "delete@example.com")
	port := s.httpPort()

	// Register first.
	body, _ := json.Marshal(pushTokenRequest{Token: "aabbccdd", Platform: "apns"})
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", port), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Delete.
	body, _ = json.Marshal(pushTokenRequest{Token: "aabbccdd"})
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("http://127.0.0.1:%d/api/push-tokens", port), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 on delete, got %d", resp.StatusCode)
	}

	// Verify it's gone.
	var userID string
	err = s.db.Rx(context.Background(), func(_ context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, "delete@example.com").Scan(&userID)
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := withRxRes1(s, context.Background(), (*exedb.Queries).GetPushTokensByUserID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected 0 push tokens after delete, got %d", len(tokens))
	}
}
