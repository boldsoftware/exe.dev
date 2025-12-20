package testinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/go4org/hashtriemap"
)

// EmailServer describes the fake email server used for testing.
type EmailServer struct {
	Port int // HTTP server port

	verbose  bool            // whether to log
	inbox    inboxMap        // email address -> inbox channel
	poisoned poisonedMap     // email address -> panic on receive
	server   *http.Server    // HTTP server
	storedMu sync.RWMutex    // protects stored
	stored   []*EmailMessage // all email messages
}

// EmailMessage is a single email message.
type EmailMessage struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// inboxMap is the type of the mapping from an email address
// to a channel of email messages.
type inboxMap = hashtriemap.HashTrieMap[string, chan *EmailMessage]

// poisonedMap is the type of the mapping for email addresses
// that should panic on receive.
type poisonedMap = hashtriemap.HashTrieMap[string, bool]

// StartEmailServer starts a new fake email server.
// This speaks HTTP, accepting emails as a POST on /
// and returning them via GET /emails.
// Emails are also sent on channels, so tests can use WaitForEmail
// to receive email.
func StartEmailServer(ctx context.Context, verbose bool) (*EmailServer, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, fmt.Errorf("StartEmailServer failed to listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	es := &EmailServer{
		Port:    port,
		verbose: verbose,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", es.handleSendEmail)
	mux.HandleFunc("GET /emails", es.handleGetEmails)

	es.server = &http.Server{Handler: mux}

	go func() {
		if err := es.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.WarnContext(ctx, "email server HTTP server failed", "error", err)
		}
	}()

	return es, nil
}

// handleSendEmail handles sending an email.
// All email messages are stored in the EmailServer,
// and they are also sent on a channel associated with the To address.
func (es *EmailServer) handleSendEmail(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var email EmailMessage
	if err := json.Unmarshal(body, &email); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if email.To == "" {
		http.Error(w, "to field is required", http.StatusBadRequest)
		return
	}

	if es.verbose {
		slog.InfoContext(r.Context(), "email received", "to", email.To, "subject", email.Subject, "body", email.Body)
	}

	if _, poisoned := es.poisoned.Load(email.To); poisoned {
		slog.ErrorContext(r.Context(), "email sent to poisoned box", "to", email.To, "subject", email.Subject, "body", email.Body)
		panic("email sent to poisoned inbox")
	}

	// Store the message for HTTP retrieval.
	es.storedMu.Lock()
	es.stored = append(es.stored, &email)
	es.storedMu.Unlock()

	// Send the message on a channel for the address.
	es.inboxChannel(email.To) <- &email
}

// handleGetEmails handles fetching all email messages.
// The to parameter permits selecting an address.
func (es *EmailServer) handleGetEmails(w http.ResponseWriter, r *http.Request) {
	toFilter := r.URL.Query().Get("to")

	es.storedMu.RLock()
	defer es.storedMu.RUnlock()

	var result []*EmailMessage
	for _, email := range es.stored {
		if toFilter == "" || email.To == toFilter {
			result = append(result, email)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, fmt.Sprintf("json encoding error: %v", err), http.StatusInternalServerError)
	}
}

// inboxChannel returns the inbox channel for the given email address.
func (es *EmailServer) inboxChannel(to string) chan *EmailMessage {
	ch, _ := es.inbox.LoadOrStore(to, make(chan *EmailMessage, 16))
	return ch
}

// WaitForEmail waits for an email to a specific address with a timeout.
func (es *EmailServer) WaitForEmail(to string) (*EmailMessage, error) {
	ch := es.inboxChannel(to)
	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("WaitForEmail timed out waiting for email to %s", to)
	}
}

// PoisonInbox marks an inbox as poisoned.
// Any email sent to this address will panic.
// This is used to verify no email is sent without
// requiring a sleep-based timeout.
func (es *EmailServer) PoisonInbox(to string) {
	es.poisoned.Store(to, true)
}

// Stop stops the email server.
func (es *EmailServer) Stop(ctx context.Context) {
	if err := es.server.Close(); err != nil {
		slog.ErrorContext(ctx, "email http server close failed", "error", err)
	}
}

// verificationRE is a regular expression to find the
// verification URL in an email message.
var verificationRE = regexp.MustCompile(`http://[^/]+/verify-(email|device)\?[^\s]+`)

// ExtractVerificationToken extracts the full verification URL from
// the email body.
func ExtractVerificationToken(body string) (string, error) {
	// Look for the full verification URL pattern including any query parameters
	// The URL continues until whitespace or end of line
	matches := verificationRE.FindStringSubmatch(body)
	if len(matches) < 1 {
		return "", fmt.Errorf("verification URL not found in email body: %s", body)
	}
	return matches[0], nil // Return the full URL including all query params
}
