package execore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// captureSupportEmail sets up a fake email server that captures emails.
func captureSupportEmail(t *testing.T, s *Server) <-chan map[string]any {
	t.Helper()
	ch := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		ch <- m
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	s.fakeHTTPEmail = srv.URL
	return ch
}

func postSupport(t *testing.T, s *Server, cookie, subject, body string, files map[string][]byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if subject != "" {
		_ = mw.WriteField("subject", subject)
	}
	if body != "" {
		_ = mw.WriteField("body", body)
	}
	for name, data := range files {
		fw, err := mw.CreateFormFile("attachments", name)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mw.Close()

	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/profile/support", s.httpPort()), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/profile/support: %v", err)
	}
	return resp
}

func TestAPIProfileSupportSendsEmailWithReplyToAndAttachment(t *testing.T) {
	s := newTestServer(t)
	const userEmail = "user@example.com"
	cookie := createTestUserWithCookie(t, s, userEmail)
	emailCh := captureSupportEmail(t, s)

	resp := postSupport(t, s, cookie, "Help me!", "I can't log in.", map[string][]byte{
		"log.txt": []byte("some logs"),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	select {
	case e := <-emailCh:
		if got, want := e["subject"], "Help me!"; got != want {
			t.Errorf("subject=%v want %v", got, want)
		}
		if got := e["reply_to"]; got != userEmail {
			t.Errorf("reply_to=%v want %v", got, userEmail)
		}
		to, _ := e["to"].(string)
		if to == "" {
			t.Errorf("to empty")
		}
		body, _ := e["body"].(string)
		if !bytes.Contains([]byte(body), []byte(userEmail)) {
			t.Errorf("body should include requesting user's email: %q", body)
		}
		atts, _ := e["attachments"].([]any)
		if len(atts) != 1 {
			t.Fatalf("want 1 attachment, got %d", len(atts))
		}
		m, _ := atts[0].(map[string]any)
		if m["filename"] != "log.txt" {
			t.Errorf("attachment filename=%v", m["filename"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for email")
	}
}

func TestAPIProfileSupportRejectsEmptySubject(t *testing.T) {
	s := newTestServer(t)
	const userEmail = "user2@example.com"
	cookie := createTestUserWithCookie(t, s, userEmail)
	resp := postSupport(t, s, cookie, "", "body", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}
