package testinfra

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestEmailServer(t *testing.T) {
	es, err := StartEmailServer(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	const (
		to      = "test@exe.dev"
		subject = "test email subject"
		body    = "test email body"
	)
	emailData := map[string]string{
		"to":      to,
		"subject": subject,
		"body":    body,
	}

	jsonData, err := json.Marshal(emailData)
	if err != nil {
		t.Fatal(err)
	}

	addr := fmt.Sprintf("http://localhost:%d", es.Port)
	resp, err := http.Post(addr, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("posting email returned unexpected status %d", resp.StatusCode)
	}

	resp.Body.Close()

	fetch := func(addr string) []*EmailMessage {
		resp, err := http.Get(addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: unexpected status %d", addr, resp.StatusCode)
		}

		var ret []*EmailMessage
		if err := json.NewDecoder(resp.Body).Decode(&ret); err != nil {
			t.Fatalf("%s: JSON decoding failed: %v", addr, err)
		}

		return ret
	}

	check := func(addr string, msgs []*EmailMessage) {
		if len(msgs) != 1 {
			t.Errorf("%s returned %d messages, want 1", addr, len(msgs))
		}
		if len(msgs) > 0 && (msgs[0].To != to || msgs[0].Subject != subject || msgs[0].Body != body) {
			t.Errorf("%s returned %q %q %q, want %q %q %q", addr, msgs[0].To, msgs[0].Subject, msgs[0].Body, to, subject, body)
		}
	}

	a := addr + "/emails"
	msgs := fetch(a)
	check(a, msgs)

	a = addr + "/emails?to=" + url.QueryEscape(to)
	msgs = fetch(a)
	check(a, msgs)

	a = addr + "/emails?to=nonexistent"
	msgs = fetch(a)
	if len(msgs) != 0 {
		t.Errorf("%s returned %d messages, expected 0", a, len(msgs))
	}

	msg, err := es.WaitForEmail(to)
	if err != nil {
		t.Error(err)
	} else {
		check("WaitForEmail", []*EmailMessage{msg})
	}
}
