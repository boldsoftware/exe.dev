package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"github.com/stripe/stripe-go/v85"
)

// Credits is the interface for credit operations on a billing account.
type Credits interface {
	GiftCredits(ctx context.Context, billingID string, p *GiftCreditsParams) error
	SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) error
	CreditBalance(ctx context.Context, billingID string) (tender.Value, error)
	GetCreditState(ctx context.Context, billingID string) (*CreditState, error)
	ListGifts(ctx context.Context, billingID string) ([]GiftEntry, error)
}

var _ Credits = (*Manager)(nil)

// GiftCreditsParams contains the parameters for gifting credits to an account.
// Callers provide a dollar amount and a gift prefix; the billing package handles
// conversion to microcents and construction of the full gift_id.
type GiftCreditsParams struct {
	// AmountUSD is the gift amount in US dollars. Must be positive.
	AmountUSD float64
	// GiftPrefix is the gift type prefix (e.g. GiftPrefixSignup, GiftPrefixDebug).
	GiftPrefix string
	// Note is an optional human-readable reason for the gift.
	// Defaults to "Credit gift from support@exe.dev" if empty.
	Note string
}

// CreditState holds the breakdown of credits for an account.
type CreditState struct {
	Paid  tender.Value
	Gift  tender.Value
	Used  tender.Value
	Total tender.Value
}

// GiftEntry represents a single gift credit entry.
type GiftEntry struct {
	Amount    tender.Value
	Note      string
	GiftID    string
	CreatedAt time.Time
}

// BuyCreditsParams contains the parameters for purchasing credits.
type BuyCreditsParams struct {
	// Email is the customer's email address.
	Email string

	// The amount of credits to purchase, in microcents. Must be positive.
	Amount tender.Value

	// SuccessURL is the URL to redirect to after successful checkout.
	SuccessURL string

	// CancelURL is the URL to redirect to if checkout is canceled.
	CancelURL string
}

// ReceiptInfo holds a receipt URL and the charge creation time.
type ReceiptInfo struct {
	URL     string
	Created time.Time
}

// GiftCredits inserts a gift credit for the given billing account.
// The gift_id is constructed as "<prefix>:<billingID>:<nanos>" for uniqueness.
// The operation is idempotent: a duplicate gift_id is silently ignored.
func (m *Manager) GiftCredits(ctx context.Context, billingID string, p *GiftCreditsParams) error {
	if p.AmountUSD <= 0 {
		return fmt.Errorf("gift amount must be positive, got %v", p.AmountUSD)
	}
	if p.GiftPrefix == "" {
		return errors.New("gift prefix is required")
	}

	amount := tender.Mint(int64(p.AmountUSD*100), 0)

	// Signup gifts use "signup:<billingID>" with no timestamp suffix,
	// ensuring at most one signup bonus per account via the unique index
	// on gift_id. Other gift types append nanoseconds so the same
	// prefix+account can receive multiple gifts.
	var giftID string
	if p.GiftPrefix == GiftPrefixSignup {
		giftID = fmt.Sprintf("%s:%s", p.GiftPrefix, billingID)
	} else {
		giftID = fmt.Sprintf("%s:%s:%d", p.GiftPrefix, billingID, time.Now().UnixNano())
	}

	note := p.Note
	if note == "" {
		note = "Credit gift from support@exe.dev"
	}

	return exedb.WithTx1(m.DB, ctx, (*exedb.Queries).GiftCredits, exedb.GiftCreditsParams{
		AccountID: billingID,
		Amount:    amount.Microcents(),
		GiftID:    &giftID,
		Note:      &note,
	})
}

// GetCreditState returns the credit breakdown for the given billing account.
// Used is stored as an absolute (positive) value even though usage rows are negative in the DB.
func (m *Manager) GetCreditState(ctx context.Context, billingID string) (*CreditState, error) {
	row, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetCreditState, billingID)
	if err != nil {
		return nil, fmt.Errorf("get credit state: %w", err)
	}
	s := &CreditState{
		Gift:  tender.Mint(0, row.Gift),
		Paid:  tender.Mint(0, row.Paid),
		Total: tender.Mint(0, row.Total),
		// Used is stored as negative in DB; return absolute value.
		Used: tender.Mint(0, -row.Used),
	}
	return s, nil
}

// ListGifts returns all gift credits for the given billing account, ordered by
// most recent first. Returns an empty (non-nil) slice if no gifts exist.
func (m *Manager) ListGifts(ctx context.Context, billingID string) ([]GiftEntry, error) {
	rows, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).ListGiftCredits, billingID)
	if err != nil {
		return nil, fmt.Errorf("list gifts: %w", err)
	}
	gifts := make([]GiftEntry, len(rows))
	for i, r := range rows {
		gifts[i] = GiftEntry{
			Amount:    tender.Mint(0, r.Amount),
			CreatedAt: r.CreatedAt,
		}
		if r.Note != nil {
			gifts[i].Note = *r.Note
		}
		if r.GiftID != nil {
			gifts[i].GiftID = *r.GiftID
		}
	}
	return gifts, nil
}

// SpendCredits deducts credits from the given billing account.
func (m *Manager) SpendCredits(ctx context.Context, billingID string, quantity int, unitPrice tender.Value) error {
	if unitPrice.IsNegative() {
		return fmt.Errorf("unit price must be non-negative, got %d microcents", unitPrice.Microcents())
	}

	creditType := "usage"
	return exedb.WithTx1(m.DB, ctx, (*exedb.Queries).UseCredits, exedb.UseCreditsParams{
		AccountID:  billingID,
		Amount:     unitPrice.Times(-quantity).Microcents(),
		CreditType: &creditType,
	})
}

// CreditBalance returns the current credit balance for the given billing account.
func (m *Manager) CreditBalance(ctx context.Context, billingID string) (tender.Value, error) {
	bal, err := exedb.WithRxRes1(m.DB, ctx, (*exedb.Queries).GetCreditBalance, billingID)
	if err != nil {
		return tender.Zero(), err
	}
	return tender.Mint(0, bal), nil
}

// BuyCredits creates a Stripe checkout session for a one-time credit purchase.
// It returns the checkout URL for the customer to complete payment.
func (m *Manager) BuyCredits(ctx context.Context, billingID string, p *BuyCreditsParams) (checkoutURL string, _ error) {
	if p.Amount.IsWorthless() {
		return "", fmt.Errorf("amount must be positive, got %d", p.Amount)
	}

	c := m.client()

	params := &stripe.CheckoutSessionCreateParams{
		Customer:           &billingID,
		Mode:               new("payment"),
		PaymentMethodTypes: []*string{new("card")},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency: new("usd"),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
						Name: new("Account Credits"),
					},
					UnitAmount: new(p.Amount.Cents()),
				},
				Quantity: new(int64(1)),
			},
		},
		SuccessURL: &p.SuccessURL,
		CancelURL:  &p.CancelURL,
	}
	params.AddMetadata("type", "credit_purchase")
	params.PaymentIntentData = &stripe.CheckoutSessionCreatePaymentIntentDataParams{
		Metadata: map[string]string{
			"type": "credit_purchase",
		},
	}

	sess, err := c.V1CheckoutSessions.Create(ctx, params)
	if isNotExists(err) {
		return "", fmt.Errorf("%w: stripe customer with billing ID %q", ErrNotFound, billingID)
	}
	if err != nil {
		return "", err
	}

	var requestID string
	if sess.LastResponse != nil {
		requestID = sess.LastResponse.RequestID
	}
	m.slog().InfoContext(ctx, "credit checkout session created",
		"stripe_request_id", requestID,
		"session_id", sess.ID,
		"billing_id", billingID,
		"cents", p.Amount,
	)
	return sess.URL, nil
}

// SyncCredits polls Stripe for completed credit-purchase payments
// and records them in the database. Each event is processed idempotently.
func (m *Manager) SyncCredits(ctx context.Context, since time.Time) error {
	c := m.client()

	params := &stripe.PaymentIntentListParams{
		CreatedRange: &stripe.RangeQueryParams{
			GreaterThan: since.Unix(),
		},
	}

	for intent, err := range c.V1PaymentIntents.List(ctx, params).All(ctx) {
		if err != nil {
			return fmt.Errorf("list payment intents: %w", err)
		}

		if intent.Status != stripe.PaymentIntentStatusSucceeded {
			continue
		}
		if intent.Metadata["type"] != "credit_purchase" {
			continue
		}

		amount := tender.Mint(intent.Amount, 0)
		stripeEventID := intent.ID
		err := exedb.WithTx1(m.DB, ctx, (*exedb.Queries).InsertPaidCredits, exedb.InsertPaidCreditsParams{
			AccountID:     intent.Customer.ID,
			Amount:        amount.Microcents(),
			StripeEventID: &stripeEventID,
		})
		if err != nil {
			return fmt.Errorf("insert credit ledger entry: %w", err)
		}

		m.slog().InfoContext(ctx, "credit purchase synced",
			"pi_id", intent.ID,
			"billing_id", intent.Customer.ID,
			"amount", amount,
		)
	}
	return nil
}

// ReceiptURLs returns a map of payment intent ID to Stripe receipt URL
// for all succeeded credit-purchase charges belonging to the given customer.
func (m *Manager) ReceiptURLs(ctx context.Context, customerID string) (map[string]string, error) {
	c := m.client()

	params := &stripe.ChargeListParams{
		Customer: stripe.String(customerID),
	}

	result := make(map[string]string)
	for charge, err := range c.V1Charges.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list charges: %w", err)
		}
		if charge.PaymentIntent == nil || charge.ReceiptURL == "" {
			continue
		}
		if charge.Metadata["type"] != "credit_purchase" {
			continue
		}
		result[charge.PaymentIntent.ID] = charge.ReceiptURL
	}
	return result, nil
}

// ReceiptURLsAfter returns receipt URLs for credit-purchase charges created at or after since.
func (m *Manager) ReceiptURLsAfter(ctx context.Context, customerID string, since time.Time) ([]ReceiptInfo, error) {
	c := m.client()
	params := &stripe.ChargeListParams{
		Customer:     stripe.String(customerID),
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	var result []ReceiptInfo
	for charge, err := range c.V1Charges.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list charges: %w", err)
		}
		if charge.ReceiptURL == "" || charge.Metadata["type"] != "credit_purchase" {
			continue
		}
		result = append(result, ReceiptInfo{URL: charge.ReceiptURL, Created: time.Unix(charge.Created, 0).UTC()})
	}
	return result, nil
}
