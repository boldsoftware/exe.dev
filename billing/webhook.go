package billing

import (
	"context"
	"io"
	"net/http"

	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"
)

// subscriptionEventTypes are routed to stripe_webhook_events for the subscription pipeline.
// Any other event type lands in other_stripe_events for out-of-band post-processing;
// add a sibling routing branch here if a new category needs special in-process handling.
var subscriptionEventTypes = map[stripe.EventType]bool{
	"customer.subscription.created": true,
	"customer.subscription.updated": true,
	"customer.subscription.deleted": true,
}

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
		if subscriptionEventTypes[event.Type] {
			return q.InsertStripeWebhookEvent(ctx, exedb.InsertStripeWebhookEventParams{
				StripeEventID: event.ID,
				EventType:     string(event.Type),
				Payload:       string(body),
			})
		}
		var apiVersion *string
		if event.APIVersion != "" {
			v := event.APIVersion
			apiVersion = &v
		}
		return q.InsertOtherStripeEvent(ctx, exedb.InsertOtherStripeEventParams{
			StripeEventID:   event.ID,
			EventType:       string(event.Type),
			APIVersion:      apiVersion,
			StripeCreatedAt: event.Created,
			Source:          "webhook",
			Payload:         string(body),
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
