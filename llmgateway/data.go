package llmgateway

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// GatewayData is an interface for retrieving data needed by the LLM gateway.
// This is used so that both exed and exeprox can use the LLM gateway;
// exed will use the database directly, while exeprox will ask exed.
type GatewayData interface {
	// BoxCreator takes a box name and returns the user ID
	// that created the box. The bool result reports whether
	// the box exists.
	BoxCreator(ctx context.Context, boxName string) (string, bool, error)

	// CheckAndRefreshCredit takes a user ID and checks if the user
	// has any credit available (after refresh).
	// This returns the refreshed credit info.
	// This updates the database with the refreshed credit amount.
	// If now is not the zero time, it is used as the current time,
	// for testing purposes.
	CheckAndRefreshCredit(ctx context.Context, userID string, now time.Time) (*CreditInfo, error)

	// TopUpOnBillingUpgrade applies a one-time billing-upgrade bonus.
	// The implementation must be idempotent across retries/concurrency.
	TopUpOnBillingUpgrade(ctx context.Context, userID string, now time.Time) error

	// DebitCredit subtracts the given cost (in USD) from the user's credit.
	// This returns the new credit info after the debit.
	DebitCredit(ctx context.Context, userID string, costUSD float64, now time.Time) (*CreditInfo, error)

	// AccountIDForUser takes a user ID and returns its account ID.
	// The bool result reports whether an account exists.
	AccountIDForUser(ctx context.Context, userID string) (string, bool, error)

	// UseCredits applies a credit usage entry and returns remaining billing credit.
	UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) (tender.Value, error)
}

// DBGatewayData is an implementation of GatewayData that uses
// the database directly.
type DBGatewayData struct {
	DB *sqlite.DB
}

// BoxCreator takes a box name and returns the user ID
// that created the box. This implements [GatewayData].
func (gd *DBGatewayData) BoxCreator(ctx context.Context, boxName string) (string, bool, error) {
	box, err := exedb.WithRxRes1(gd.DB, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return box.CreatedByUserID, true, nil
}

// CheckAndRefreshCredit implements [CreditManager.CheckAndRefreshCredit].
func (gd *DBGatewayData) CheckAndRefreshCredit(ctx context.Context, userID string, now time.Time) (*CreditInfo, error) {
	return CheckAndRefreshCreditDB(ctx, gd.DB, userID, now)
}

// TopUpOnBillingUpgrade implements [CreditManager.TopUpOnBillingUpgrade].
func (gd *DBGatewayData) TopUpOnBillingUpgrade(ctx context.Context, userID string, now time.Time) error {
	return TopUpOnBillingUpgradeDB(ctx, gd.DB, userID, now)
}

// DebitCredit implements [CreditManager.DebitCredit].
func (gd *DBGatewayData) DebitCredit(ctx context.Context, userID string, costUSD float64, now time.Time) (*CreditInfo, error) {
	return DebitCreditDB(ctx, gd.DB, userID, costUSD, now)
}

// AccountIDForUser implements [GatewayData.AccountIDForUser].
func (gd *DBGatewayData) AccountIDForUser(ctx context.Context, userID string) (string, bool, error) {
	account, err := exedb.WithRxRes1(gd.DB, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return account.ID, true, nil
}

// UseCredits implements [GatewayData.UseCredits].
func (gd *DBGatewayData) UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) (tender.Value, error) {
	return (&billing.Manager{DB: gd.DB}).SpendCredits(ctx, accountID, quantity, unitPrice)
}
