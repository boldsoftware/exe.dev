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

// BoxInfo contains identifying information about a box.
type BoxInfo struct {
	ID        int
	Name      string
	CreatorID string
}

// BoxUsage contains per-request usage data to be recorded alongside a debit.
type BoxUsage struct {
	BoxID          int
	Provider       string
	Model          string
	CostMicrocents int64 // USD microcents (1/1,000,000 of a dollar)
}

// GatewayData is an interface for retrieving data needed by the LLM gateway.
// This is used so that both exed and exeprox can use the LLM gateway;
// exed will use the database directly, while exeprox will ask exed.
type GatewayData interface {
	// BoxLookup returns information about a box.
	// Returns nil, nil if the box does not exist.
	BoxLookup(ctx context.Context, boxName string) (*BoxInfo, error)

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
	// If boxUsage is non-nil, box LLM usage is also recorded.
	DebitCredit(ctx context.Context, userID string, costUSD float64, now time.Time, boxUsage *BoxUsage) (*CreditInfo, error)

	// AccountIDForUser takes a user ID and returns its account ID.
	// The bool result reports whether an account exists.
	AccountIDForUser(ctx context.Context, userID string) (string, bool, error)

	// TeamBillingAccountID returns the billing account ID of the user's
	// team billing_owner. The bool result reports whether the user is in
	// a team with a billing_owner who has a billing account.
	TeamBillingAccountID(ctx context.Context, userID string) (string, bool, error)

	// UseCredits applies a credit usage entry.
	UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) error

	// GetCreditBalance returns the current billing credit balance for an account.
	GetCreditBalance(ctx context.Context, accountID string) (tender.Value, error)
}

// DBGatewayData is an implementation of GatewayData that uses
// the database directly.
type DBGatewayData struct {
	DB *sqlite.DB
}

// BoxLookup implements [GatewayData].
func (gd *DBGatewayData) BoxLookup(ctx context.Context, boxName string) (*BoxInfo, error) {
	box, err := exedb.WithRxRes1(gd.DB, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &BoxInfo{
		ID:        box.ID,
		Name:      box.Name,
		CreatorID: box.CreatedByUserID,
	}, nil
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
func (gd *DBGatewayData) DebitCredit(ctx context.Context, userID string, costUSD float64, now time.Time, boxUsage *BoxUsage) (*CreditInfo, error) {
	return DebitCreditDB(ctx, gd.DB, userID, costUSD, now, boxUsage)
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

// TeamBillingAccountID implements [GatewayData.TeamBillingAccountID].
func (gd *DBGatewayData) TeamBillingAccountID(ctx context.Context, userID string) (string, bool, error) {
	accountID, err := exedb.WithRxRes1(gd.DB, ctx, (*exedb.Queries).GetTeamBillingOwnerAccountID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return accountID, true, nil
}

// UseCredits implements [GatewayData.UseCredits].
func (gd *DBGatewayData) UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) error {
	return (&billing.Manager{DB: gd.DB}).SpendCredits(ctx, accountID, quantity, unitPrice)
}

// GetCreditBalance implements [GatewayData.GetCreditBalance].
func (gd *DBGatewayData) GetCreditBalance(ctx context.Context, accountID string) (tender.Value, error) {
	return (&billing.Manager{DB: gd.DB}).CreditBalance(ctx, accountID)
}
