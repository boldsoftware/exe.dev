package accounting

// This file contains functions, types and constants to help exed check and track
// credits and debits for various kinds of usage-based accounting.
import (
	"context"
	"time"
)

type UsageDebit struct {
	Usage
	Model            string    `json:"model"`
	MessageID        string    `json:"message_id"`
	BillingAccountID string    `json:"billing_account_id"`
	Created          time.Time `json:"created"`
}

// UsageCredit represents a credit purchase by a user
type UsageCredit struct {
	ID               int64     `json:"id,omitempty"`
	BillingAccountID string    `json:"billing_account_id"`
	Amount           float64   `json:"amount"`
	Created          time.Time `json:"created"`
	PaymentMethod    string    `json:"payment_method"`
	PaymentID        string    `json:"payment_id"`
	Status           string    `json:"status"`
	Data             any       `json:"data,omitempty"` // Additional data specific to payment method
}

// Usage represents billing and rate-limit usage.
type Usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

// Accountant handles credit balance checking and usage debiting
type Accountant interface {
	// GetUserBalance retrieves the current credit balance for a user
	GetUserBalance(ctx context.Context, billingAccountID string) (float64, error)

	// DebitUsage records a usage debit for a user
	DebitUsage(ctx context.Context, debit UsageDebit) error

	// DebitUsage records a usage credit for a user
	CreditUsage(ctx context.Context, credit UsageCredit) error

	HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any)

	ApplyNewUserCredits(ctx context.Context, billingAccountID string) any
}
