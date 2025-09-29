package accounting

// This file contains functions, types and constants to help exed check and track
// credits and debits for various kinds of usage-based accounting.
import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
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
type Accountant struct{}

// BillingAccountForBox retrieves the billing account ID for a given box name.
// Box belongs to Alloc, which has a BillingAccountID.
// So when an Alloc is created, it should also be provided a BillingAccountID.
func (a *Accountant) BillingAccountForBox(ctx context.Context, rx *sqlite.Rx, boxName string) (string, error) {
	queries := exedb.New(rx.Conn())
	box, err := queries.BoxNamed(ctx, boxName)
	if err != nil {
		return "", fmt.Errorf("failed to get box by name: %w", err)
	}
	ba, err := queries.GetBillingAccountByAllocID(ctx, box.AllocID)
	if err != nil {
		return "", fmt.Errorf("failed to billing account for alloc ID %s: %w", box.AllocID, err)
	}
	return ba.BillingAccountID, nil
}

func NewAccountant() *Accountant {
	return &Accountant{}
}

// ApplyNewUserCredits applies new user credits within a transaction.
func (a *Accountant) ApplyNewUserCredits(ctx context.Context, tx *sqlite.Tx, billingAccountID string) error {
	queries := exedb.New(tx.Conn())

	// Record the credit
	err := queries.InsertUsageCredit(ctx, exedb.InsertUsageCreditParams{
		BillingAccountID: billingAccountID,
		Amount:           100.0,
		PaymentMethod:    "new_user_credit",
		PaymentID:        fmt.Sprintf("new_user_%s_%d", billingAccountID, time.Now().Unix()),
		Status:           "completed",
		Data:             nil,
	})
	if err != nil {
		return err
	}

	// Mark as applied to prevent duplicate credits
	return queries.RecordUserEvent(ctx, exedb.RecordUserEventParams{
		UserID: billingAccountID,
		Event:  "new_user_credits_applied",
	})
}

// CreditUsage records a usage credit within a transaction.
func (a *Accountant) CreditUsage(ctx context.Context, tx *sqlite.Tx, credit UsageCredit) error {
	queries := exedb.New(tx.Conn())

	// Serialize data if provided
	var dataStr *string
	if credit.Data != nil {
		dataBytes, err := json.Marshal(credit.Data)
		if err != nil {
			return fmt.Errorf("failed to marshal credit data: %w", err)
		}
		dataJSON := string(dataBytes)
		dataStr = &dataJSON
	}

	return queries.InsertUsageCredit(ctx, exedb.InsertUsageCreditParams{
		BillingAccountID: credit.BillingAccountID,
		Amount:           credit.Amount,
		PaymentMethod:    credit.PaymentMethod,
		PaymentID:        credit.PaymentID,
		Status:           credit.Status,
		Data:             dataStr,
	})
}

// DebitUsage records a usage debit within a transaction.
func (a *Accountant) DebitUsage(ctx context.Context, tx *sqlite.Tx, debit UsageDebit) error {
	queries := exedb.New(tx.Conn())

	// Check for duplicate message_id to prevent double billing
	if debit.MessageID != "" {
		_, err := queries.GetUsageDebitByMessageID(ctx, debit.MessageID)
		if err == nil {
			// Debit already exists, skip
			return nil
		}
	}

	return queries.InsertUsageDebit(ctx, exedb.InsertUsageDebitParams{
		BillingAccountID:         debit.BillingAccountID,
		Model:                    debit.Model,
		MessageID:                debit.MessageID,
		InputTokens:              int64(debit.InputTokens),
		CacheCreationInputTokens: int64(debit.CacheCreationInputTokens),
		CacheReadInputTokens:     int64(debit.CacheReadInputTokens),
		OutputTokens:             int64(debit.OutputTokens),
		CostUsd:                  debit.CostUSD,
	})
}

// GetBalance retrieves the current balance for a billing account within a read transaction.
func (a *Accountant) GetBalance(ctx context.Context, rx *sqlite.Rx, billingAccountID string) (float64, error) {
	queries := exedb.New(rx.Conn())

	// Get total credits
	creditsInterface, err := queries.GetCreditBalanceForBillingAccount(ctx, billingAccountID)
	if err != nil {
		return 0, fmt.Errorf("failed to get credits: %w", err)
	}

	// Get total debits
	debitsInterface, err := queries.GetDebitBalanceForBillingAccount(ctx, billingAccountID)
	if err != nil {
		return 0, fmt.Errorf("failed to get debits: %w", err)
	}

	// Convert interface{} to float64
	credits, ok := creditsInterface.(float64)
	if !ok {
		return 0, fmt.Errorf("credits value is not a float64")
	}

	debits, ok := debitsInterface.(float64)
	if !ok {
		return 0, fmt.Errorf("debits value is not a float64")
	}

	return credits - debits, nil
}

func (a *Accountant) HasNewUserCredits(ctx context.Context, rx *sqlite.Rx, billingAccountID string) (bool, error) {
	// Use user events to track new user credit eligibility
	queries := exedb.New(rx.Conn())

	// Check if user has the "new_user_credits_applied" event
	countInterface, err := queries.GetUserEventCount(ctx, exedb.GetUserEventCountParams{
		UserID: billingAccountID, // Using billing account ID as proxy
		Event:  "new_user_credits_applied",
	})
	if err != nil {
		return false, fmt.Errorf("failed to get user event count: %w", err)
	}

	// Convert interface{} to int64
	count, ok := countInterface.(int64)
	if !ok {
		return false, fmt.Errorf("event count is not int64")
	}

	// If no credits applied yet, user is eligible
	return count == 0, nil
}
