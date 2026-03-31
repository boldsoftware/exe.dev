package billing

import (
	"context"
	"io"
	"net/http"

	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v82/webhook"
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
	event, err := webhook.ConstructEvent(body, sig, m.WebhookSecret)
	if err != nil {
		m.slog().ErrorContext(ctx, "failed to verify webhook signature", "error", err)
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
