package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85/webhook"
)

func TestHandleWebhook(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{
		DB:            db,
		Logger:        tslog.Slogger(t),
		WebhookSecret: "whsec_test_secret",
	}

	payload := `{"id":"evt_test123","object":"event","api_version":"2026-03-25.dahlia","type":"customer.subscription.created","data":{"object":{}},"livemode":false,"created":1234567890}`
	timestamp := time.Now()
	signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(payload),
		Secret:    m.WebhookSecret,
		Timestamp: timestamp,
	})

	req := httptest.NewRequest(http.MethodPost, "/stripe/webhook", strings.NewReader(string(signedPayload.Payload)))
	req.Header.Set("Stripe-Signature", signedPayload.Header)
	rec := httptest.NewRecorder()

	m.HandleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	var events []exedb.StripeWebhookEvent
	err := exedb.WithRx(db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		var err error
		events, err = q.ListStripeWebhookEventsByType(ctx, exedb.ListStripeWebhookEventsByTypeParams{
			EventType: "customer.subscription.created",
			Limit:     10,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].StripeEventID != "evt_test123" {
		t.Errorf("expected event ID evt_test123, got %s", events[0].StripeEventID)
	}
}

func TestHandleWebhook_InvalidSignature(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{
		DB:            db,
		Logger:        tslog.Slogger(t),
		WebhookSecret: "whsec_test_secret",
	}

	payload := `{"id":"evt_test123","object":"event","api_version":"2026-03-25.dahlia","type":"customer.subscription.created","data":{"object":{}},"livemode":false,"created":1234567890}`
	req := httptest.NewRequest(http.MethodPost, "/stripe/webhook", strings.NewReader(payload))
	req.Header.Set("Stripe-Signature", "invalid_signature")
	rec := httptest.NewRecorder()

	m.HandleWebhook(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleWebhook_Idempotent(t *testing.T) {
	db := newTestDB(t)
	m := &Manager{
		DB:            db,
		Logger:        tslog.Slogger(t),
		WebhookSecret: "whsec_test_secret",
	}

	payload := `{"id":"evt_idempotent","object":"event","api_version":"2026-03-25.dahlia","type":"invoice.paid","data":{"object":{}},"livemode":false,"created":1234567890}`
	timestamp := time.Now()
	signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(payload),
		Secret:    m.WebhookSecret,
		Timestamp: timestamp,
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/stripe/webhook", strings.NewReader(string(signedPayload.Payload)))
		req.Header.Set("Stripe-Signature", signedPayload.Header)
		rec := httptest.NewRecorder()
		m.HandleWebhook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i+1, rec.Code)
		}
	}

	ctx := context.Background()
	count, err := exedb.WithRxRes1(db, ctx, (*exedb.Queries).CountOtherStripeEventsByID, "evt_idempotent")
	if err != nil {
		t.Fatalf("failed to count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 event after 3 identical requests, got %d", count)
	}
}
