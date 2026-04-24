package billing

import (
	"testing"
	"time"

	"exe.dev/billing/tender"
	"github.com/stripe/stripe-go/v85"
)

func TestExtractPaymentMethodInfo(t *testing.T) {
	tests := []struct {
		name string
		pm   *stripe.PaymentMethod
		want *PaymentMethodInfo
	}{
		{name: "nil", pm: nil, want: nil},
		{
			name: "visa card",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeCard,
				Card: &stripe.PaymentMethodCard{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025},
			},
			want: &PaymentMethodInfo{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2025, DisplayLabel: "Visa •••• 4242"},
		},
		{
			name: "amex",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeCard,
				Card: &stripe.PaymentMethodCard{Brand: "amex", Last4: "1234", ExpMonth: 6, ExpYear: 2026},
			},
			want: &PaymentMethodInfo{Type: "card", Brand: "amex", Last4: "1234", ExpMonth: 6, ExpYear: 2026, DisplayLabel: "American Express •••• 1234"},
		},
		{
			name: "link",
			pm: &stripe.PaymentMethod{
				Type: stripe.PaymentMethodTypeLink,
				Link: &stripe.PaymentMethodLink{Email: "user@example.com"},
			},
			want: &PaymentMethodInfo{Type: "link", Email: "user@example.com", DisplayLabel: "Link (user@example.com)"},
		},
		{
			name: "paypal",
			pm: &stripe.PaymentMethod{
				Type:   stripe.PaymentMethodTypePaypal,
				Paypal: &stripe.PaymentMethodPaypal{PayerEmail: "pay@example.com"},
			},
			want: &PaymentMethodInfo{Type: "paypal", Email: "pay@example.com", DisplayLabel: "PayPal (pay@example.com)"},
		},
		{
			name: "unknown sepa_debit",
			pm:   &stripe.PaymentMethod{Type: "sepa_debit"},
			want: &PaymentMethodInfo{Type: "sepa_debit", DisplayLabel: "Sepa Debit"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPaymentMethodInfo(tt.pm)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %+v", tt.want)
			}
			if got.Type != tt.want.Type || got.Brand != tt.want.Brand || got.Last4 != tt.want.Last4 ||
				got.ExpMonth != tt.want.ExpMonth || got.ExpYear != tt.want.ExpYear ||
				got.Email != tt.want.Email || got.DisplayLabel != tt.want.DisplayLabel {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReceiptURLsAfterLive(t *testing.T) {
	m := newTestManager(t)
	clock := m.startClock(t)
	ctx := t.Context()

	billingID := "exe_receipt_after_" + clock.ID()
	if err := m.upsertCustomer(ctx, billingID, "receipts@example.com"); err != nil {
		t.Fatalf("upsertCustomer: %v", err)
	}

	before := time.Now()

	// Before purchase: no receipts.
	got, err := m.ReceiptURLsAfter(ctx, billingID, before.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter before purchase: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 receipts before purchase, got %d", len(got))
	}

	// Create a credit purchase charge.
	if err := stripeCompleteCreditPurchase(ctx, m, billingID, "pm_card_visa", tender.Mint(100, 0)); err != nil {
		t.Fatalf("stripeCompleteCreditPurchase: %v", err)
	}

	// Since before the purchase: should return 1 receipt.
	got, err = m.ReceiptURLsAfter(ctx, billingID, before.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter after purchase: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(got))
	}
	if got[0].URL == "" {
		t.Fatal("receipt URL is empty")
	}
	if got[0].Created.IsZero() {
		t.Fatal("receipt Created is zero")
	}

	// Since well after the purchase: should return 0 receipts.
	got, err = m.ReceiptURLsAfter(ctx, billingID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ReceiptURLsAfter with future since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 receipts for future since, got %d", len(got))
	}
}

func TestFormatCardLabel(t *testing.T) {
	tests := []struct{ brand, last4, want string }{
		{"visa", "4242", "Visa •••• 4242"},
		{"mastercard", "5555", "Mastercard •••• 5555"},
		{"amex", "1234", "American Express •••• 1234"},
		{"diners", "9999", "Diners Club •••• 9999"},
		{"jcb", "0000", "JCB •••• 0000"},
		{"unknown", "1111", "Unknown •••• 1111"},
		{"visa", "", "Visa"},
	}
	for _, tt := range tests {
		if got := formatCardLabel(tt.brand, tt.last4); got != tt.want {
			t.Errorf("formatCardLabel(%q,%q) = %q, want %q", tt.brand, tt.last4, got, tt.want)
		}
	}
}

func TestFormatPaymentTypeLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"sepa_debit", "Sepa Debit"},
		{"us_bank_account", "Us Bank Account"},
		{"card", "Card"},
		{"amazon_pay", "Amazon Pay"},
	}
	for _, tt := range tests {
		if got := formatPaymentTypeLabel(tt.in); got != tt.want {
			t.Errorf("formatPaymentTypeLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
