package billing

import (
	"context"
	"io"
	"net/http"

	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v85/webhook"
)

// HandleWebhook receives Stripe webhook events, verifies the signature,
// and stores the raw payload for later processing.
func (m *Manager) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to read webhook body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	if m.WebhookSecret == "" {
		m.slog().ErrorContext(ctx, "webhook secret not configured")
		http.Error(w, "webhook secret not configured", http.StatusInternalServerError)
		return
	}
	if sig == "" {
		m.slog().ErrorContext(ctx, "missing Stripe-Signature header")
		http.Error(w, "missing signature", http.StatusBadRequest)
		return
	}
	event, err := webhook.ConstructEvent(body, sig, m.WebhookSecret)
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to verify webhook signature",
			"error", err,
			"has_secret", m.WebhookSecret != "",
			"has_signature", sig != "",
			"body_len", len(body))
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	err = exedb.WithTx(m.DB, ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.InsertStripeWebhookEvent(ctx, exedb.InsertStripeWebhookEventParams{
			StripeEventID: event.ID,
			EventType:     string(event.Type),
			Payload:       string(body),
		})
	})
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to store webhook event", "error", err, "event_id", event.ID)
		http.Error(w, "failed to store event", http.StatusInternalServerError)
		return
	}

	m.slog().InfoContext(ctx, "stored webhook event", "event_id", event.ID, "event_type", event.Type)
	w.WriteHeader(http.StatusOK)
}
