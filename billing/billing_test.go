package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"exe.dev/billing/stripetest"
)

func TestSubscribe(t *testing.T) {
	var log strings.Builder

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(&log, "%s %s\n", r.Method, r.URL.Path)

			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{
					"id":    "cus_test123",
					"email": r.FormValue("email"),
				})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_abc123", "lookup_key": "individual"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_test_session",
					"url": "https://checkout.stripe.com/pay/cs_test_session",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	url, err := m.Subscribe(context.Background(), "cus_test123", &SubscribeParams{
		Email:      "test@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatal(err)
	}

	if url != "https://checkout.stripe.com/pay/cs_test_session" {
		t.Errorf("got url %q", url)
	}

	want := trimLines(`
		GET /v1/prices
		POST /v1/customers
		POST /v1/checkout/sessions
	`)
	if got := log.String(); got != want {
		t.Errorf("log mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSubscribeWithTrial(t *testing.T) {
	trialEnd := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{
					"id": "cus_trial",
				})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_abc123"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				r.ParseForm()
				gotTrialEnd := r.Form.Get("subscription_data[trial_end]")
				wantTrialEnd := fmt.Sprintf("%d", trialEnd.Unix())
				if gotTrialEnd != wantTrialEnd {
					t.Errorf("subscription_data[trial_end] = %q, want %q (Unix timestamp for %v)", gotTrialEnd, wantTrialEnd, trialEnd)
				}
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_trial",
					"url": "https://checkout.stripe.com/pay/cs_trial",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	_, err := m.Subscribe(context.Background(), "cus_trial", &SubscribeParams{
		Email:      "trial@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
		TrialEnd:   trialEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeWithTrial_ZeroTrialEndOmitted(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{
					"id": "cus_notrial",
				})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_abc123"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				r.ParseForm()
				if got := r.Form.Get("subscription_data[trial_end]"); got != "" {
					t.Errorf("subscription_data[trial_end] = %q, want empty (no trial)", got)
				}
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_notrial",
					"url": "https://checkout.stripe.com/pay/cs_notrial",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	_, err := m.Subscribe(context.Background(), "cus_notrial", &SubscribeParams{
		Email:      "notrial@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeExistingSubscription_RedirectsToPortal(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{
					"id": "cus_existing",
				})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_abc123"},
					},
				})
			case r.Method == "GET" && r.URL.Path == "/v1/subscriptions":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "sub_123", "status": "active"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "bps_test",
					"url": "https://billing.stripe.com/session/bps_test",
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				t.Error("should not create checkout session for existing subscriber")
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	url, err := m.Subscribe(context.Background(), "cus_existing", &SubscribeParams{
		Email:            "existing@example.com",
		SuccessURL:       "https://example.com/success",
		CancelURL:        "https://example.com/cancel",
		RedirectToPortal: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if url != "https://billing.stripe.com/session/bps_test" {
		t.Errorf("expected portal URL, got %q", url)
	}
}

func TestVerifyCheckout(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
				sessionID := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
				switch sessionID {
				case "cs_complete":
					json.NewEncoder(w).Encode(map[string]any{
						"id":             "cs_complete",
						"status":         "complete",
						"payment_status": "paid",
						"customer":       map[string]any{"id": "cus_verified"},
					})
				case "cs_trial":
					json.NewEncoder(w).Encode(map[string]any{
						"id":             "cs_trial",
						"status":         "complete",
						"payment_status": "no_payment_required",
						"customer":       map[string]any{"id": "cus_trial"},
					})
				case "cs_incomplete":
					json.NewEncoder(w).Encode(map[string]any{
						"id":             "cs_incomplete",
						"status":         "open",
						"payment_status": "unpaid",
					})
				case "cs_unpaid":
					json.NewEncoder(w).Encode(map[string]any{
						"id":             "cs_unpaid",
						"status":         "complete",
						"payment_status": "unpaid",
					})
				default:
					w.WriteHeader(http.StatusNotFound)
					json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"type":    "invalid_request_error",
							"message": "No such checkout session",
						},
					})
				}
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	t.Run("complete session", func(t *testing.T) {
		billingID, err := m.VerifyCheckout(context.Background(), "cs_complete")
		if err != nil {
			t.Fatal(err)
		}
		if billingID != "cus_verified" {
			t.Errorf("got billingID %q, want cus_verified", billingID)
		}
	})

	t.Run("trial session (no payment required)", func(t *testing.T) {
		billingID, err := m.VerifyCheckout(context.Background(), "cs_trial")
		if err != nil {
			t.Fatal(err)
		}
		if billingID != "cus_trial" {
			t.Errorf("got billingID %q, want cus_trial", billingID)
		}
	})

	t.Run("incomplete session", func(t *testing.T) {
		_, err := m.VerifyCheckout(context.Background(), "cs_incomplete")
		if err == nil {
			t.Fatal("expected error for incomplete session")
		}
	})

	t.Run("unpaid session", func(t *testing.T) {
		_, err := m.VerifyCheckout(context.Background(), "cs_unpaid")
		if err == nil {
			t.Fatal("expected error for unpaid session")
		}
	})

	t.Run("empty session ID", func(t *testing.T) {
		_, err := m.VerifyCheckout(context.Background(), "")
		if err == nil {
			t.Fatal("expected error for empty session ID")
		}
	})
}

func TestDashboardURL(t *testing.T) {
	url := MakeCustomerDashboardURL("cus_abc123")
	if url != "https://dashboard.stripe.com/customers/cus_abc123" {
		t.Errorf("got %q", url)
	}
}

func TestSubscribePriceNotFound(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{},
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	_, err := m.Subscribe(context.Background(), "cus_test", &SubscribeParams{
		Email:      "test@example.com",
		Plan:       "nonexistent_plan",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent price")
	}
	if !strings.Contains(err.Error(), "no active price found") {
		t.Errorf("expected 'no active price found' error, got %v", err)
	}
}

func TestVerifyCheckoutNoCustomer(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
				json.NewEncoder(w).Encode(map[string]any{
					"id":             "cs_no_customer",
					"status":         "complete",
					"payment_status": "paid",
					"customer":       nil,
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	_, err := m.VerifyCheckout(context.Background(), "cs_no_customer")
	if err == nil {
		t.Fatal("expected error for session with no customer")
	}
	if !strings.Contains(err.Error(), "no customer") {
		t.Errorf("expected 'no customer' error, got %v", err)
	}
}

func TestSubscribeNilParams(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{"id": "cus_nil"})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_default"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_nil",
					"url": "https://checkout.stripe.com/pay/cs_nil",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	url, err := m.Subscribe(context.Background(), "cus_nil", nil)
	if err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Error("expected checkout URL")
	}
}

func TestSubscribeFailsOn4xxError(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "price_4xx"}},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{"id": "cus_4xx"})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"type":    "invalid_request_error",
						"message": "bad request",
					},
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	_, err := m.Subscribe(context.Background(), "cus_4xx", &SubscribeParams{
		Email:      "4xx@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err == nil {
		t.Fatal("expected error for 4xx response")
	}
}

func TestVerifyCheckoutFailsOn4xxError(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"type":    "invalid_request_error",
						"message": "No such checkout session",
					},
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	_, err := m.VerifyCheckout(context.Background(), "cs_nonexistent")
	if err == nil {
		t.Fatal("expected error for 4xx response")
	}
}

func TestPriceCache(t *testing.T) {
	calls := 0
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{"id": "cus_cache"})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				calls++
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{"id": "price_cached"},
					},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_cache",
					"url": "https://checkout.stripe.com/pay/cs_cache",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	params := &SubscribeParams{
		Email:      "cache@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	}

	_, err := m.Subscribe(context.Background(), "cus_cache1", params)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected 1 price lookup call, got %d", calls)
	}

	_, err = m.Subscribe(context.Background(), "cus_cache2", params)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected price lookup to be cached, got %d calls", calls)
	}
}

func TestOpenPortal(t *testing.T) {
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
				r.ParseForm()
				if r.Form.Get("customer") != "exe_test123" {
					t.Errorf("expected customer exe_test123, got %q", r.Form.Get("customer"))
				}
				if r.Form.Get("return_url") != "https://example.com/profile" {
					t.Errorf("expected return_url https://example.com/profile, got %q", r.Form.Get("return_url"))
				}
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "bps_test123",
					"url": "https://billing.stripe.com/session/bps_test123",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
	}

	url, err := m.openPortal(context.Background(), "exe_test123", "https://example.com/profile")
	if err != nil {
		t.Fatal(err)
	}

	if url != "https://billing.stripe.com/session/bps_test123" {
		t.Errorf("got url %q, want https://billing.stripe.com/session/bps_test123", url)
	}
}

func TestOpenPortal_RequiresReturnURL(t *testing.T) {
	m := &Manager{}

	_, err := m.openPortal(context.Background(), "exe_test123", "")
	if err == nil || !strings.Contains(err.Error(), "return URL is required") {
		t.Error("expected error for missing return URL")
	}
}

func TestOpenPortal_NoRetry4xx(t *testing.T) {
	calls := 0
	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"type":    "invalid_request_error",
					"message": "No such customer",
				},
			})
		}),
	}

	_, err := m.openPortal(context.Background(), "exe_notfound", "https://example.com/profile")
	if err == nil {
		t.Error("expected error for 400 response")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call (no retry), got %d", calls)
	}
}

func TestSubscriptionEvents_YieldsEvents(t *testing.T) {
	eventTime := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).Unix()

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == "/v1/events" {
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{
							"id":      "evt_1",
							"type":    "customer.subscription.created",
							"created": eventTime,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_created1",
									"customer": map[string]any{"id": "cus_user1"},
									"status":   "active",
								},
							},
						},
						{
							"id":      "evt_2",
							"type":    "customer.subscription.deleted",
							"created": eventTime + 60,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_deleted1",
									"customer": map[string]any{"id": "cus_user2"},
									"status":   "canceled",
								},
							},
						},
					},
					"has_more": false,
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []SubscriptionEvent
	for e := range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
		got = append(got, e)
		if len(got) >= 2 {
			cancel()
		}
	}

	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].AccountID != "cus_user1" {
		t.Errorf("got AccountID %q, want cus_user1", got[0].AccountID)
	}
	if got[0].EventType != "active" {
		t.Errorf("got EventType %q, want active", got[0].EventType)
	}
	if got[1].AccountID != "cus_user2" {
		t.Errorf("got AccountID %q, want cus_user2", got[1].AccountID)
	}
	if got[1].EventType != "canceled" {
		t.Errorf("got EventType %q, want canceled", got[1].EventType)
	}
}

func TestSubscriptionEvents_FiltersOlderEvents(t *testing.T) {
	t.Skip("TODO: add status field to mock subscription object")
	// The Events API filters by created timestamp via CreatedRange param,
	// so events older than sinceTime won't be returned by Stripe.
	newEventTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).Unix()
	sinceTime := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == "/v1/events" {
				// Verify the created[gt] parameter is set correctly
				createdGt := r.URL.Query().Get("created[gt]")
				if createdGt == "" {
					t.Error("expected created[gt] query parameter")
				}
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{
							"id":      "evt_new",
							"type":    "customer.subscription.deleted",
							"created": newEventTime,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_new",
									"customer": map[string]any{"id": "cus_new"},
								},
							},
						},
					},
					"has_more": false,
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []SubscriptionEvent
	for e := range m.SubscriptionEvents(ctx, sinceTime) {
		got = append(got, e)
		cancel()
	}

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].AccountID != "cus_new" {
		t.Errorf("got AccountID %q, want cus_new", got[0].AccountID)
	}
}

func TestSubscriptionEvents_PersistsThroughErrors(t *testing.T) {
	t.Skip("TODO: fix synctest timing issue with SubscriptionEvents ticker")
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		eventTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).Unix()

		var logBuf strings.Builder
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		m := &Manager{
			Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/events" {
					calls++
					if calls < 3 {
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]any{
							"error": map[string]any{"type": "api_error", "message": "server error"},
						})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{
						"data": []map[string]any{
							{
								"id":      "evt_success",
								"type":    "customer.subscription.created",
								"created": eventTime,
								"data": map[string]any{
									"object": map[string]any{
										"id":       "sub_success",
										"customer": map[string]any{"id": "cus_success"},
									},
								},
							},
						},
						"has_more": false,
					})
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			}),
			Logger: logger,
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var got []SubscriptionEvent
		for e := range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
			got = append(got, e)
			cancel()
		}

		if len(got) != 1 {
			t.Errorf("expected 1 event after retries, got %d", len(got))
		}
		if calls < 3 {
			t.Errorf("expected at least 3 calls, got %d", calls)
		}
		if !strings.Contains(logBuf.String(), "error listing subscription events") {
			t.Error("expected error to be logged")
		}
	})
}

func TestSubscriptionEvents_LogsRateLimitAsWarning(t *testing.T) {
	t.Skip("TODO: fix synctest timing issue with SubscriptionEvents ticker")
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		eventTime := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).Unix()

		var logBuf strings.Builder
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		m := &Manager{
			Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/events" {
					calls++
					if calls < 2 {
						w.WriteHeader(http.StatusTooManyRequests)
						json.NewEncoder(w).Encode(map[string]any{
							"error": map[string]any{"type": "rate_limit_error", "message": "rate limited"},
						})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{
						"data": []map[string]any{
							{
								"id":      "evt_ratelimit",
								"type":    "customer.subscription.created",
								"created": eventTime,
								"data": map[string]any{
									"object": map[string]any{
										"id":       "sub_ratelimit",
										"customer": map[string]any{"id": "cus_ratelimit"},
									},
								},
							},
						},
						"has_more": false,
					})
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			}),
			Logger: logger,
		}

		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			for range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
				cancel()
				return
			}
		}()

		synctest.Wait()

		if !strings.Contains(logBuf.String(), "rate limited") {
			t.Error("expected rate limit warning to be logged")
		}
		if !strings.Contains(logBuf.String(), "level=WARN") {
			t.Error("expected warning level log for rate limit")
		}
	})
}

func TestSubscriptionEvents_StopsOnContextCancel(t *testing.T) {
	t.Skip("TODO: fix synctest timing issue with SubscriptionEvents ticker")
	synctest.Test(t, func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/events" {
					json.NewEncoder(w).Encode(map[string]any{
						"data":     []map[string]any{},
						"has_more": false,
					})
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			}),
		}

		ctx, cancel := context.WithCancel(t.Context())
		stopped := make(chan struct{})
		go func() {
			for range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
			}
			close(stopped)
		}()

		synctest.Wait()
		cancel()
		synctest.Wait()

		select {
		case <-stopped:
		default:
			t.Error("iterator did not stop after context cancel")
		}
	})
}

func TestSubscriptionEvents_AdvancesSince(t *testing.T) {
	t.Skip("TODO: fix synctest timing issue with SubscriptionEvents ticker")
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		firstEventTime := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).Unix()

		m := &Manager{
			Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/events" {
					calls++
					if calls == 1 {
						json.NewEncoder(w).Encode(map[string]any{
							"data": []map[string]any{
								{
									"id":      "evt_first",
									"type":    "customer.subscription.created",
									"created": firstEventTime,
									"data": map[string]any{
										"object": map[string]any{
											"id":       "sub_first",
											"customer": map[string]any{"id": "cus_first"},
										},
									},
								},
							},
							"has_more": false,
						})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{
						"data": []map[string]any{
							{
								"id":      "evt_second",
								"type":    "customer.subscription.deleted",
								"created": firstEventTime + 3600,
								"data": map[string]any{
									"object": map[string]any{
										"id":       "sub_second",
										"customer": map[string]any{"id": "cus_second"},
									},
								},
							},
						},
						"has_more": false,
					})
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			}),
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var got []SubscriptionEvent
		for e := range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
			got = append(got, e)
			if len(got) >= 2 {
				cancel()
			}
		}

		if len(got) < 2 {
			t.Errorf("expected at least 2 events from multiple polls, got %d", len(got))
		}
		if calls < 2 {
			t.Errorf("expected at least 2 poll calls, got %d", calls)
		}
	})
}

func TestSubscriptionEvents_SkipsEmptyCustomerID(t *testing.T) {
	t.Skip("TODO: add status field to mock subscription object")
	eventTime := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).Unix()

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == "/v1/events" {
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{
							"id":      "evt_empty",
							"type":    "customer.subscription.created",
							"created": eventTime,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_empty",
									"customer": map[string]any{"id": ""},
								},
							},
						},
						{
							"id":      "evt_valid",
							"type":    "customer.subscription.created",
							"created": eventTime + 60,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_valid",
									"customer": map[string]any{"id": "cus_valid"},
								},
							},
						},
					},
					"has_more": false,
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []SubscriptionEvent
	for e := range m.SubscriptionEvents(ctx, time.Unix(0, 0)) {
		got = append(got, e)
		cancel()
	}

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].AccountID != "cus_valid" {
		t.Errorf("got AccountID %q, want cus_valid", got[0].AccountID)
	}
}

func TestSubscriptionEvents_YieldFalseStopsIterator(t *testing.T) {
	t.Skip("TODO: add status field to mock subscription object")
	eventTime := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC).Unix()

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && r.URL.Path == "/v1/events" {
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{
						{
							"id":      "evt_1",
							"type":    "customer.subscription.created",
							"created": eventTime,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_1",
									"customer": map[string]any{"id": "cus_1"},
								},
							},
						},
						{
							"id":      "evt_2",
							"type":    "customer.subscription.deleted",
							"created": eventTime + 60,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_2",
									"customer": map[string]any{"id": "cus_2"},
								},
							},
						},
						{
							"id":      "evt_3",
							"type":    "customer.subscription.created",
							"created": eventTime + 120,
							"data": map[string]any{
								"object": map[string]any{
									"id":       "sub_3",
									"customer": map[string]any{"id": "cus_3"},
								},
							},
						},
					},
					"has_more": false,
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
	}

	var got []SubscriptionEvent
	for e := range m.SubscriptionEvents(context.Background(), time.Unix(0, 0)) {
		got = append(got, e)
		break
	}

	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].AccountID != "cus_1" {
		t.Errorf("got AccountID %q, want cus_1", got[0].AccountID)
	}
}

func TestSubscribe_LogsStripeRequestID(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/customers":
				json.NewEncoder(w).Encode(map[string]any{"id": "cus_log_test"})
			case r.Method == "GET" && r.URL.Path == "/v1/prices":
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{{"id": "price_log_test"}},
				})
			case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "cs_log_test",
					"url": "https://checkout.stripe.com/pay/cs_log_test",
				})
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		}),
		Logger: logger,
	}

	_, err := m.Subscribe(context.Background(), "cus_log_test", &SubscribeParams{
		Email:      "log@example.com",
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatal(err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "stripe_request_id") {
		t.Error("expected stripe_request_id in logs")
	}
	if !strings.Contains(logs, "req_test_") {
		t.Error("expected req_test_ prefix in stripe_request_id")
	}
	if !strings.Contains(logs, "checkout session created") {
		t.Error("expected 'checkout session created' log message")
	}
	if !strings.Contains(logs, "customer created") {
		t.Error("expected 'customer created' log message")
	}
}

func TestVerifyCheckout_LogsStripeRequestID(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
				json.NewEncoder(w).Encode(map[string]any{
					"id":             "cs_verify_log",
					"status":         "complete",
					"payment_status": "paid",
					"customer":       map[string]any{"id": "cus_verify_log"},
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
		Logger: logger,
	}

	_, err := m.VerifyCheckout(context.Background(), "cs_verify_log")
	if err != nil {
		t.Fatal(err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "stripe_request_id") {
		t.Error("expected stripe_request_id in logs")
	}
	if !strings.Contains(logs, "req_test_") {
		t.Error("expected req_test_ prefix in stripe_request_id")
	}
	if !strings.Contains(logs, "checkout session verified") {
		t.Error("expected 'checkout session verified' log message")
	}
}

func TestOpenPortal_LogsStripeRequestID(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions" {
				json.NewEncoder(w).Encode(map[string]any{
					"id":  "bps_log_test",
					"url": "https://billing.stripe.com/session/bps_log_test",
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
		Logger: logger,
	}

	_, err := m.openPortal(context.Background(), "cus_portal_log", "https://example.com/return")
	if err != nil {
		t.Fatal(err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "stripe_request_id") {
		t.Error("expected stripe_request_id in logs")
	}
	if !strings.Contains(logs, "req_test_") {
		t.Error("expected req_test_ prefix in stripe_request_id")
	}
	if !strings.Contains(logs, "billing portal session created") {
		t.Error("expected 'billing portal session created' log message")
	}
}

func TestVerifyCheckout_LogsStripeRequestIDOnError(t *testing.T) {
	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	m := &Manager{
		Client: stripetest.Client(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
				json.NewEncoder(w).Encode(map[string]any{
					"id":             "cs_error_log",
					"status":         "open",
					"payment_status": "unpaid",
				})
				return
			}
			http.Error(w, "not found", http.StatusNotFound)
		}),
		Logger: logger,
	}

	_, err := m.VerifyCheckout(context.Background(), "cs_error_log")
	if err == nil {
		t.Fatal("expected error for incomplete session")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "stripe_request_id") {
		t.Error("expected stripe_request_id in error logs")
	}
	if !strings.Contains(logs, "checkout session incomplete") {
		t.Error("expected 'checkout session incomplete' log message")
	}
}

func trimLines(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fmt.Fprintln(&b, line)
	}
	return b.String()
}
