// Package email provides an interface for sending emails with multiple provider implementations.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/keighl/postmark"
	"github.com/mailgun/mailgun-go/v4"
)

// Sender is an interface for sending emails.
type Sender interface {
	// Send sends an email with the given parameters.
	// from should be in the format "Name <email@example.com>"
	Send(ctx context.Context, from, to, subject, body string) error
}

// Senders holds multiple email provider implementations.
// Use Postmark or Mailgun fields directly to choose which provider to use per-email.
type Senders struct {
	Postmark       Sender
	Mailgun        Sender
	preferMailgun  bool
}

// NewSendersFromEnv creates Senders from environment variables.
// For Postmark: requires POSTMARK_API_KEY
// For Mailgun: requires MAILGUN_API_KEY; domain is passed as parameter
// EMAIL_PROVIDER controls which provider Any() prefers: "mailgun" or "postmark" (default)
// Either or both may be nil if not configured.
func NewSendersFromEnv(mailgunDomain string) *Senders {
	s := &Senders{}

	if apiKey := os.Getenv("POSTMARK_API_KEY"); apiKey != "" {
		s.Postmark = NewPostmarkSender(apiKey)
	}

	if apiKey := os.Getenv("MAILGUN_API_KEY"); apiKey != "" && mailgunDomain != "" {
		s.Mailgun = NewMailgunSender(mailgunDomain, apiKey)
	}

	s.preferMailgun = os.Getenv("EMAIL_PROVIDER") == "mailgun"

	return s
}

// Any returns a configured sender based on EMAIL_PROVIDER preference.
// Returns nil if no sender is configured.
func (s *Senders) Any() Sender {
	if s.preferMailgun {
		if s.Mailgun != nil {
			return s.Mailgun
		}
		return s.Postmark
	}
	if s.Postmark != nil {
		return s.Postmark
	}
	return s.Mailgun
}

// PostmarkSender implements Sender using Postmark.
type PostmarkSender struct {
	client *postmark.Client
}

// NewPostmarkSender creates a new Postmark sender.
func NewPostmarkSender(apiKey string) *PostmarkSender {
	client := postmark.NewClient(apiKey, "")
	// Under load, we see HTTP/2 GOAWAY errors from Postmark.
	// Instead of attempting to cache bodies and enable retries, disable HTTP/2 entirely.
	client.HTTPClient.Transport = &http.Transport{
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	return &PostmarkSender{client: client}
}

// Send sends an email via Postmark.
func (s *PostmarkSender) Send(ctx context.Context, from, to, subject, body string) error {
	email := postmark.Email{
		From:     from,
		To:       to,
		Subject:  subject,
		TextBody: body,
	}
	_, err := s.client.SendEmail(email)
	return err
}

// MailgunSender implements Sender using Mailgun.
type MailgunSender struct {
	mg     *mailgun.MailgunImpl
	domain string
}

// NewMailgunSender creates a new Mailgun sender.
func NewMailgunSender(domain, apiKey string) *MailgunSender {
	return &MailgunSender{
		mg:     mailgun.NewMailgun(domain, apiKey),
		domain: domain,
	}
}

// Send sends an email via Mailgun.
func (s *MailgunSender) Send(ctx context.Context, from, to, subject, body string) error {
	msg := s.mg.NewMessage(from, subject, body, to)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, _, err := s.mg.Send(ctx, msg)
	if err != nil {
		return fmt.Errorf("mailgun send failed: %w", err)
	}
	return nil
}
