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
type Accountant interface {
	// GetUserBalance retrieves the current credit balance for a user
	GetUserBalance(ctx context.Context, billingAccountID string) (float64, error)

	// DebitUsage records a usage debit for a user
	DebitUsage(ctx context.Context, debit UsageDebit) error

	// DebitUsage records a usage credit for a user
	CreditUsage(ctx context.Context, credit UsageCredit) error

	HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any)

	ApplyNewUserCredits(ctx context.Context, billingAccountID string) any

	BillingAccountForBox(ctx context.Context, boxName string) (string, error)
}

type dbAccountant struct {
	db *sqlite.DB
}

// BillingAccountForBox implements Accountant.
// Box belongs to Alloc, which has a BillingAccountID.
// So when an Alloc is created, it should also be provided a BillingAccountID.
func (d *dbAccountant) BillingAccountForBox(ctx context.Context, boxName string) (string, error) {
	ret := ""
	err := d.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		box, err := queries.BoxNamed(ctx, boxName)
		if err != nil {
			return fmt.Errorf("failed to get box by name: %w", err)
		}
		ba, err := queries.GetBillingAccountByAllocID(ctx, box.AllocID)
		if err != nil {
			return fmt.Errorf("failed to billing account for alloc ID %s: %w", box.AllocID, err)
		}
		ret = ba.BillingAccountID
		return nil
	})
	return ret, err
}

func NewDBAccountant(db *sqlite.DB) Accountant {
	return &dbAccountant{
		db: db,
	}
}

// ApplyNewUserCredits implements Accountant.
func (d *dbAccountant) ApplyNewUserCredits(ctx context.Context, billingAccountID string) any {
	return d.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		// Record the credit
		err := queries.InsertUsageCredit(ctx, exedb.InsertUsageCreditParams{
			BillingAccountID: billingAccountID,
			Amount:           10.0,
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
	})
}

// CreditUsage implements Accountant.
func (d *dbAccountant) CreditUsage(ctx context.Context, credit UsageCredit) error {
	return d.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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
	})
}

// DebitUsage implements Accountant.
func (d *dbAccountant) DebitUsage(ctx context.Context, debit UsageDebit) error {
	return d.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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
	})
}

// GetUserBalance implements Accountant.
func (d *dbAccountant) GetUserBalance(ctx context.Context, billingAccountID string) (float64, error) {
	var balance float64

	err := d.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())

		// Get total credits
		creditsInterface, err := queries.GetCreditBalanceForBillingAccount(ctx, billingAccountID)
		if err != nil {
			return fmt.Errorf("failed to get credits: %w", err)
		}

		// Get total debits
		debitsInterface, err := queries.GetDebitBalanceForBillingAccount(ctx, billingAccountID)
		if err != nil {
			return fmt.Errorf("failed to get debits: %w", err)
		}

		// Convert interface{} to float64
		credits, ok := creditsInterface.(float64)
		if !ok {
			return fmt.Errorf("credits value is not a float64")
		}

		debits, ok := debitsInterface.(float64)
		if !ok {
			return fmt.Errorf("debits value is not a float64")
		}

		balance = credits - debits
		return nil
	})

	if err != nil {
		return 0, err
	}

	return balance, nil
}

// HasNewUserCredits implements Accountant.
func (d *dbAccountant) HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any) {
	// Use user events to track new user credit eligibility
	var hasCredits bool

	err := d.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())

		// Check if user has the "new_user_credits_applied" event
		countInterface, err := queries.GetUserEventCount(ctx, exedb.GetUserEventCountParams{
			UserID: billingAccountID, // Using billing account ID as proxy
			Event:  "new_user_credits_applied",
		})
		if err != nil {
			return fmt.Errorf("failed to get user event count: %w", err)
		}

		// Convert interface{} to int64
		count, ok := countInterface.(int64)
		if !ok {
			return fmt.Errorf("event count is not int64")
		}

		// If no credits applied yet, user is eligible
		hasCredits = count == 0
		return nil
	})

	if err != nil {
		return false, nil
	}

	return hasCredits, map[string]interface{}{
		"amount": 10.0, // $10 in new user credits
		"reason": "new_user_signup",
	}
}

var _ Accountant = &dbAccountant{}
