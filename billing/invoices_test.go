package billing

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"exe.dev/billing/stripetest"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v85"
)

func TestParseInvoiceLinePlanName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1 × Individual Plan (at $20.00 / month)", "Individual"},
		{"1 × Team Plan (at $50.00 / month)", "Team"},
		{"1 x Business Plan (at $100.00 / year)", "Business"},
		{"Individual Plan", "Individual"},
		{"Remaining time on Individual Plan (XLarge) after 22 Apr 2026", "Individual Plan (XLarge)"},
		{"Unused time on Individual Plan (Small) after 22 Apr 2026", "Individual Plan (Small)"},
		{"Something else entirely", "Something else entirely"},
		{"", ""},
	}
	for _, tt := range tests {
		got := parseInvoiceLinePlanName(tt.in)
		if got != tt.want {
			t.Errorf("parseInvoiceLinePlanName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInvoiceLineInfo(t *testing.T) {
	tests := []struct {
		name     string
		lines    []*stripe.InvoiceLineItem
		wantPlan string
	}{
		{
			name:     "single line item",
			lines:    []*stripe.InvoiceLineItem{{Description: "1 × Individual Plan (at $20.00 / month)", Amount: 2000}},
			wantPlan: "Individual",
		},
		{
			name: "proration picks highest amount and appends suffix",
			lines: []*stripe.InvoiceLineItem{
				{Description: "Unused time on Individual Plan (Small) after 22 Apr 2026", Amount: -1913},
				{Description: "Remaining time on Individual Plan (XLarge) after 22 Apr 2026", Amount: 15306},
			},
			wantPlan: "Individual Plan (XLarge) - Prorated",
		},
		{
			name:     "no lines",
			lines:    nil,
			wantPlan: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines *stripe.InvoiceLineItemList
			if tt.lines != nil {
				lines = &stripe.InvoiceLineItemList{Data: tt.lines}
			}
			planName, _, _ := invoiceLineInfo(lines, 0, 0)
			if planName != tt.wantPlan {
				t.Errorf("planName = %q, want %q", planName, tt.wantPlan)
			}
		})
	}
}

func TestUpcomingInvoice(t *testing.T) {
	now := time.Now().UTC()
	periodStart := now
	periodEnd := now.Add(30 * 24 * time.Hour)

	// activeSubResponse returns a handler that responds to /v1/subscriptions with
	// an active subscription, then delegates to invoiceHandler for the preview call.
	activeSubResponse := func(invoiceHandler func(w http.ResponseWriter, r *http.Request)) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/subscriptions" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"object":"list","data":[{"id":"sub_test123","status":"active"}],"has_more":false}`)
				return
			}
			invoiceHandler(w, r)
		}
	}

	tests := []struct {
		name              string
		handler           func(w http.ResponseWriter, r *http.Request)
		wantNil           bool
		wantPlanName      string
		wantDescription   string
		wantAmount        int64
		wantSubtotal      int64
		wantCreditApplied int64
		wantStatus        string
	}{
		{
			name: "valid upcoming invoice with line items",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","subtotal":2000,"amount_due":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (at $20.00 / month)","period":{"start":%d,"end":%d}}]}}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:    "Individual",
			wantDescription: "Upcoming",
			wantAmount:      2000,
			wantSubtotal:    2000,
			wantStatus:      "upcoming",
		},
		{
			name: "upcoming invoice with credit applied",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","subtotal":4000,"amount_due":0,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (Medium) (at $40.00 / month)","period":{"start":%d,"end":%d}}]}}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:      "Individual Plan (Medium)",
			wantDescription:   "Upcoming",
			wantAmount:        0,
			wantSubtotal:      4000,
			wantCreditApplied: 4000,
			wantStatus:        "upcoming",
		},
		{
			name: "no subscription returns nil",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/subscriptions" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"object":"list","data":[],"has_more":false}`)
					return
				}
				w.WriteHeader(404)
			},
			wantNil: true,
		},
		{
			name: "empty line items uses invoice-level period",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","amount_due":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[]}}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:    "",
			wantDescription: "Upcoming",
			wantAmount:      2000,
			wantStatus:      "upcoming",
		},
		{
			name: "line item with empty description",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","amount_due":3500,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"","period":{"start":%d,"end":%d}}]}}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:    "",
			wantDescription: "Upcoming",
			wantAmount:      3500,
			wantStatus:      "upcoming",
		},
		{
			name: "zero amount upcoming invoice",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","amount_due":0,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (at $20.00 / month)","period":{"start":%d,"end":%d}}]}}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:    "Individual",
			wantDescription: "Upcoming",
			wantAmount:      0,
			wantStatus:      "upcoming",
		},
		{
			name: "null lines uses invoice-level period",
			handler: activeSubResponse(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"invoice","amount_due":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			wantPlanName:    "",
			wantDescription: "Upcoming",
			wantAmount:      2000,
			wantStatus:      "upcoming",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{
				Client: stripetest.Client(t, tt.handler),
				Logger: tslog.Slogger(t),
			}

			inv, err := m.UpcomingInvoice(t.Context(), "cus_test123")
			if err != nil {
				t.Fatalf("UpcomingInvoice returned unexpected error: %v", err)
			}

			if tt.wantNil {
				if inv != nil {
					t.Fatalf("expected nil, got %+v", inv)
				}
				return
			}

			if inv == nil {
				t.Fatal("expected non-nil invoice, got nil")
			}

			if inv.Description != tt.wantDescription {
				t.Errorf("Description = %q, want %q", inv.Description, tt.wantDescription)
			}
			if inv.PlanName != tt.wantPlanName {
				t.Errorf("PlanName = %q, want %q", inv.PlanName, tt.wantPlanName)
			}
			if inv.AmountPaid != tt.wantAmount {
				t.Errorf("AmountPaid = %d, want %d", inv.AmountPaid, tt.wantAmount)
			}
			if inv.Subtotal != tt.wantSubtotal {
				t.Errorf("Subtotal = %d, want %d", inv.Subtotal, tt.wantSubtotal)
			}
			if inv.CreditApplied != tt.wantCreditApplied {
				t.Errorf("CreditApplied = %d, want %d", inv.CreditApplied, tt.wantCreditApplied)
			}
			if inv.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", inv.Status, tt.wantStatus)
			}
			if inv.PeriodStart.IsZero() {
				t.Error("PeriodStart is zero (would render as Jan 1, 1970)")
			}
			if inv.PeriodEnd.IsZero() {
				t.Error("PeriodEnd is zero (would render as Jan 1, 1970)")
			}
			if inv.Date.IsZero() {
				t.Error("Date is zero")
			}
		})
	}
}

func TestListInvoices(t *testing.T) {
	now := time.Now().UTC()
	periodStart := now.Add(-30 * 24 * time.Hour)
	periodEnd := now

	t.Run("returns paid and open invoices", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","amount_paid":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"hosted_invoice_url":"https://invoice.stripe.com/i/test1","invoice_pdf":"https://pay.stripe.com/invoice/test1/pdf","lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (at $20.00 / month)","period":{"start":%d,"end":%d}}]}},{"object":"invoice","status":"draft","amount_paid":0,"currency":"usd","created":%d,"period_start":%d,"period_end":%d},{"object":"invoice","status":"open","amount_paid":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"hosted_invoice_url":"https://invoice.stripe.com/i/test2","lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (at $20.00 / month)","period":{"start":%d,"end":%d}}]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(), periodStart.Unix(), periodEnd.Unix(),
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					now.Unix(), periodStart.Unix(), periodEnd.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		// Should skip draft invoices
		if len(invoices) != 2 {
			t.Fatalf("got %d invoices, want 2", len(invoices))
		}
		if invoices[0].Status != "paid" {
			t.Errorf("first invoice status = %q, want paid", invoices[0].Status)
		}
		if invoices[0].PlanName != "Individual" {
			t.Errorf("first invoice PlanName = %q, want Individual", invoices[0].PlanName)
		}
		if invoices[0].HostedInvoiceURL != "https://invoice.stripe.com/i/test1" {
			t.Errorf("first invoice HostedInvoiceURL = %q", invoices[0].HostedInvoiceURL)
		}
		if invoices[1].Status != "open" {
			t.Errorf("second invoice status = %q, want open", invoices[1].Status)
		}
	})

	t.Run("empty list returns nil slice", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"object":"list","data":[],"has_more":false}`)
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 0 {
			t.Fatalf("got %d invoices, want 0", len(invoices))
		}
	})

	t.Run("credit applied shows subtotal and credit amount", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Subtotal $40, but only $0 paid because credit covered it all.
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","subtotal":4000,"amount_paid":0,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (Medium) (at $40.00 / month)","period":{"start":%d,"end":%d}}]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 1 {
			t.Fatalf("got %d invoices, want 1", len(invoices))
		}
		if invoices[0].Subtotal != 4000 {
			t.Errorf("Subtotal = %d, want 4000", invoices[0].Subtotal)
		}
		if invoices[0].AmountPaid != 0 {
			t.Errorf("AmountPaid = %d, want 0", invoices[0].AmountPaid)
		}
		if invoices[0].CreditApplied != 4000 {
			t.Errorf("CreditApplied = %d, want 4000", invoices[0].CreditApplied)
		}
	})

	t.Run("partial credit applied", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Subtotal $40, $25 paid, $15 covered by credit.
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","subtotal":4000,"amount_paid":2500,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (Medium) (at $40.00 / month)","period":{"start":%d,"end":%d}}]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 1 {
			t.Fatalf("got %d invoices, want 1", len(invoices))
		}
		if invoices[0].CreditApplied != 1500 {
			t.Errorf("CreditApplied = %d, want 1500", invoices[0].CreditApplied)
		}
	})

	t.Run("downgrade proration generates credit", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Negative subtotal = downgrade credit. AmountPaid is 0.
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","subtotal":-3958,"amount_paid":0,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"Unused time on Individual Plan (Large) after 21 Apr 2026","amount":-5977,"period":{"start":%d,"end":%d}},{"description":"Remaining time on Individual Plan (Medium) after 21 Apr 2026","amount":2019,"period":{"start":%d,"end":%d}}]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix(),
					periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 1 {
			t.Fatalf("got %d invoices, want 1", len(invoices))
		}
		if invoices[0].CreditGenerated != 3958 {
			t.Errorf("CreditGenerated = %d, want 3958", invoices[0].CreditGenerated)
		}
		if invoices[0].CreditApplied != 0 {
			t.Errorf("CreditApplied = %d, want 0", invoices[0].CreditApplied)
		}
		if invoices[0].AmountPaid != 0 {
			t.Errorf("AmountPaid = %d, want 0", invoices[0].AmountPaid)
		}
	})

	t.Run("no credit applied", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Subtotal = amountPaid, no credit.
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","subtotal":2000,"amount_paid":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[{"description":"1 \u00d7 Individual Plan (at $20.00 / month)","period":{"start":%d,"end":%d}}]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 1 {
			t.Fatalf("got %d invoices, want 1", len(invoices))
		}
		if invoices[0].CreditApplied != 0 {
			t.Errorf("CreditApplied = %d, want 0", invoices[0].CreditApplied)
		}
	})

	t.Run("missing description generates fallback", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"object":"list","data":[{"object":"invoice","status":"paid","amount_paid":2000,"currency":"usd","created":%d,"period_start":%d,"period_end":%d,"lines":{"object":"list","data":[]}}],"has_more":false}`,
					now.Unix(), periodStart.Unix(), periodEnd.Unix())
			}),
			Logger: tslog.Slogger(t),
		}

		invoices, err := m.ListInvoices(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("ListInvoices: %v", err)
		}
		if len(invoices) != 1 {
			t.Fatalf("got %d invoices, want 1", len(invoices))
		}
		wantDesc := "Subscription \u2014 " + periodEnd.Format("Jan 2006")
		if invoices[0].Description != wantDesc {
			t.Errorf("Description = %q, want %q", invoices[0].Description, wantDesc)
		}
	})
}

func TestCustomerCreditBalance(t *testing.T) {
	t.Run("negative balance returns credit", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test123","object":"customer","balance":-5000}`)
			}),
			Logger: tslog.Slogger(t),
		}

		balance, err := m.CustomerCreditBalance(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("CustomerCreditBalance: %v", err)
		}
		if balance != 5000 {
			t.Errorf("balance = %d, want 5000", balance)
		}
	})

	t.Run("zero balance returns zero", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test123","object":"customer","balance":0}`)
			}),
			Logger: tslog.Slogger(t),
		}

		balance, err := m.CustomerCreditBalance(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("CustomerCreditBalance: %v", err)
		}
		if balance != 0 {
			t.Errorf("balance = %d, want 0", balance)
		}
	})

	t.Run("positive balance returns zero", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test123","object":"customer","balance":3000}`)
			}),
			Logger: tslog.Slogger(t),
		}

		balance, err := m.CustomerCreditBalance(t.Context(), "cus_test123")
		if err != nil {
			t.Fatalf("CustomerCreditBalance: %v", err)
		}
		if balance != 0 {
			t.Errorf("balance = %d, want 0", balance)
		}
	})
}

func TestCustomerDiscount(t *testing.T) {
	t.Run("percent off coupon", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test","object":"customer","discount":{"id":"di_1","object":"discount","source":{"type":"coupon","coupon":{"id":"HALF_OFF","object":"coupon","name":"50% Off","percent_off":50,"duration":"forever"}}}}`)
			}),
			Logger: tslog.Slogger(t),
		}

		di, err := m.CustomerDiscount(t.Context(), "cus_test")
		if err != nil {
			t.Fatalf("CustomerDiscount: %v", err)
		}
		if di == nil {
			t.Fatal("expected discount, got nil")
		}
		if di.CouponID != "HALF_OFF" {
			t.Errorf("CouponID = %q, want %q", di.CouponID, "HALF_OFF")
		}
		if di.Name != "50% Off" {
			t.Errorf("Name = %q, want %q", di.Name, "50% Off")
		}
		if di.PercentOff != 50 {
			t.Errorf("PercentOff = %v, want 50", di.PercentOff)
		}
		if di.Duration != "forever" {
			t.Errorf("Duration = %q, want %q", di.Duration, "forever")
		}
	})

	t.Run("amount off coupon", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test","object":"customer","discount":{"id":"di_2","object":"discount","source":{"type":"coupon","coupon":{"id":"FIVE_BUCKS","object":"coupon","name":"$5 Off","amount_off":500,"currency":"usd","duration":"repeating","duration_in_months":3}}}}`)
			}),
			Logger: tslog.Slogger(t),
		}

		di, err := m.CustomerDiscount(t.Context(), "cus_test")
		if err != nil {
			t.Fatalf("CustomerDiscount: %v", err)
		}
		if di == nil {
			t.Fatal("expected discount, got nil")
		}
		if di.CouponID != "FIVE_BUCKS" {
			t.Errorf("CouponID = %q, want %q", di.CouponID, "FIVE_BUCKS")
		}
		if di.AmountOffCents != 500 {
			t.Errorf("AmountOffCents = %d, want 500", di.AmountOffCents)
		}
		if di.Duration != "repeating" {
			t.Errorf("Duration = %q, want %q", di.Duration, "repeating")
		}
		if di.DurationInMonths != 3 {
			t.Errorf("DurationInMonths = %d, want 3", di.DurationInMonths)
		}
	})

	t.Run("no discount", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"cus_test","object":"customer"}`)
			}),
			Logger: tslog.Slogger(t),
		}

		di, err := m.CustomerDiscount(t.Context(), "cus_test")
		if err != nil {
			t.Fatalf("CustomerDiscount: %v", err)
		}
		if di != nil {
			t.Errorf("expected nil discount, got %+v", di)
		}
	})

	t.Run("customer not found", func(t *testing.T) {
		m := &Manager{
			Client: stripetest.Client(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such customer"}}`)
			}),
			Logger: tslog.Slogger(t),
		}

		di, err := m.CustomerDiscount(t.Context(), "cus_missing")
		if err != nil {
			t.Fatalf("CustomerDiscount: %v", err)
		}
		if di != nil {
			t.Errorf("expected nil discount, got %+v", di)
		}
	})
}
