package execore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/sqlite"
)

type fakePushSender struct {
	mu       sync.Mutex
	sent     []fakePush
	failWith error
}

type fakePush struct {
	DeviceToken string
	Title       string
	Body        string
	Data        map[string]string
}

func (f *fakePushSender) Send(ctx context.Context, deviceToken, title, body string, data map[string]string) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fakePush{deviceToken, title, body, data})
	return nil
}

func createPushTestBox(t *testing.T, s *Server) (userID, boxName string) {
	t.Helper()
	user, err := s.createUser(t.Context(), testSSHPubKey, "pushowner@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	boxName = "test-push-box"
	err = s.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create box: %v", err)
	}

	// Register a push token for this user.
	if err := withTx1(s, t.Context(), (*exedb.Queries).UpsertPushToken, exedb.UpsertPushTokenParams{
		UserID:   user.UserID,
		Token:    "aabbccdd",
		Platform: "apns",
	}); err != nil {
		t.Fatalf("Failed to insert push token: %v", err)
	}

	return user.UserID, boxName
}

func TestVMPushSend_Success(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	_, boxName := createPushTestBox(t, s)

	fake := &fakePushSender{}

	reqBody := exeweb.VMPushRequest{
		Title: "Agent finished",
		Body:  "Summary of work done",
		Data:  map[string]string{"conversation_id": "conv-123"},
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/_/gateway/push/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	ps := s.proxyServer()
	ps.PushSender = fake
	ps.HandleVMPushSend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMPushResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("Expected success, got error: %s", resp.Error)
	}
	if resp.Sent != 1 {
		t.Fatalf("Expected 1 sent, got %d", resp.Sent)
	}

	if len(fake.sent) != 1 {
		t.Fatalf("Expected 1 push sent, got %d", len(fake.sent))
	}
	if fake.sent[0].Title != "Agent finished" {
		t.Fatalf("Expected title 'Agent finished', got %q", fake.sent[0].Title)
	}
	if fake.sent[0].DeviceToken != "aabbccdd" {
		t.Fatalf("Expected device token 'aabbccdd', got %q", fake.sent[0].DeviceToken)
	}
}

func TestVMPushSend_MissingBox(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	fake := &fakePushSender{}

	body, _ := json.Marshal(exeweb.VMPushRequest{Title: "Test"})
	req := httptest.NewRequest("POST", "/_/gateway/push/send", bytes.NewReader(body))
	req.Header.Set("X-Exedev-Box", "nonexistent-box")
	w := httptest.NewRecorder()

	ps := s.proxyServer()
	ps.PushSender = fake
	ps.HandleVMPushSend(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestVMPushSend_NoTokens(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Create user + box but no push tokens.
	user, err := s.createUser(t.Context(), testSSHPubKey, "nopush@example.com", AllQualityChecks)
	if err != nil {
		t.Fatal(err)
	}
	boxName := "test-nopush-box"
	err = s.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		_, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakePushSender{}

	body, _ := json.Marshal(exeweb.VMPushRequest{Title: "Test"})
	req := httptest.NewRequest("POST", "/_/gateway/push/send", bytes.NewReader(body))
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	ps := s.proxyServer()
	ps.PushSender = fake
	ps.HandleVMPushSend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp exeweb.VMPushResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success || resp.Sent != 0 {
		t.Fatalf("Expected success with 0 sent, got: %+v", resp)
	}
}

func TestVMPushSend_InvalidTokenCleanup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID, boxName := createPushTestBox(t, s)

	fake := &fakePushSender{failWith: exeweb.ErrPushTokenInvalid}

	body, _ := json.Marshal(exeweb.VMPushRequest{Title: "Test"})
	req := httptest.NewRequest("POST", "/_/gateway/push/send", bytes.NewReader(body))
	req.Header.Set("X-Exedev-Box", boxName)
	w := httptest.NewRecorder()

	ps := s.proxyServer()
	ps.PushSender = fake
	ps.HandleVMPushSend(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The invalid token should have been deleted.
	tokens, err := withRxRes1(s, t.Context(), (*exedb.Queries).GetPushTokensByUserID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("Expected 0 tokens after cleanup, got %d", len(tokens))
	}
}

func TestVMPushSend_NotConfigured(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	body, _ := json.Marshal(exeweb.VMPushRequest{Title: "Test"})
	req := httptest.NewRequest("POST", "/_/gateway/push/send", bytes.NewReader(body))
	req.Header.Set("X-Exedev-Box", "whatever")
	w := httptest.NewRecorder()

	ps := s.proxyServer()
	// PushSender is nil — not configured.
	ps.HandleVMPushSend(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Expected 503, got %d: %s", w.Code, w.Body.String())
	}
}
