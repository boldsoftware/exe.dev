package llmgateway

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/llmpricing"
)

type useCreditsCall struct {
	accountID string
	quantity  int
	unitPrice tender.Value
}

type billingGatewayDataSpy struct {
	*DBGatewayData

	mu             sync.Mutex
	useCreditsErr  error
	remaining      tender.Value
	useCreditsCall []useCreditsCall
}

func (s *billingGatewayDataSpy) UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) (tender.Value, error) {
	s.mu.Lock()
	s.useCreditsCall = append(s.useCreditsCall, useCreditsCall{
		accountID: accountID,
		quantity:  quantity,
		unitPrice: unitPrice,
	})
	s.mu.Unlock()

	if s.useCreditsErr != nil {
		return tender.Zero(), s.useCreditsErr
	}
	return s.remaining, nil
}

func (s *billingGatewayDataSpy) useCreditsCalls() []useCreditsCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make([]useCreditsCall, len(s.useCreditsCall))
	copy(calls, s.useCreditsCall)
	return calls
}

func newBillingBackedTransport(t *testing.T, data *billingGatewayDataSpy, userID string, now time.Time) (*CreditManager, *accountingTransport) {
	t.Helper()
	creditMgr := NewCreditManager(data)
	creditMgr.now = func() time.Time { return now }

	transport := &accountingTransport{
		provider:         llmpricing.ProviderOpenAI,
		log:              slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})),
		creditMgr:        creditMgr,
		incomingReq:      httptest.NewRequest("POST", "/v1/chat/completions", nil),
		boxName:          "box-a",
		userID:           userID,
		billingBacked:    true,
		billingAccountID: "acct_123",
	}
	return creditMgr, transport
}

func TestAccountingTransport_BillingBacked_FreeOnlySkipsBillingCharge(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	userID := "billing-free-only-user"
	createTestUser(t, db, userID, "billing-free-only@example.com")

	now := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	data := &billingGatewayDataSpy{
		DBGatewayData: &DBGatewayData{DB: db},
		remaining:     tender.Mint(2, 0),
	}
	creditMgr, transport := newBillingBackedTransport(t, data, userID, now)

	if _, err := creditMgr.CheckAndRefreshCredit(context.Background(), userID); err != nil {
		t.Fatalf("failed to initialize credit: %v", err)
	}

	remaining := transport.debitResponseCredits(5, false)
	wantRemaining := initialFreeCreditNoSubscriptionUSD - 5
	if !floatClose(remaining, wantRemaining, 0.000001) {
		t.Fatalf("remaining = %f, want %f", remaining, wantRemaining)
	}

	calls := data.useCreditsCalls()
	if len(calls) != 0 {
		t.Fatalf("UseCredits calls = %d, want 0", len(calls))
	}
}

func TestAccountingTransport_BillingBacked_PartialOverageChargesOnlyOverage(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	userID := "billing-partial-overage-user"
	createTestUser(t, db, userID, "billing-partial-overage@example.com")

	now := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	data := &billingGatewayDataSpy{
		DBGatewayData: &DBGatewayData{DB: db},
		remaining:     tender.Mint(2, 0),
	}
	creditMgr, transport := newBillingBackedTransport(t, data, userID, now)

	ctx := context.Background()
	if _, err := creditMgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("failed to initialize credit: %v", err)
	}
	preDebit := initialFreeCreditNoSubscriptionUSD - 3
	if _, err := creditMgr.DebitCredit(ctx, userID, preDebit); err != nil {
		t.Fatalf("failed to prepare partial free balance: %v", err)
	}

	remaining := transport.debitResponseCredits(5, false)
	if !floatClose(remaining, -2, 0.000001) {
		t.Fatalf("remaining = %f, want -2", remaining)
	}

	calls := data.useCreditsCalls()
	if len(calls) != 1 {
		t.Fatalf("UseCredits calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.accountID != "acct_123" {
		t.Fatalf("accountID = %q, want %q", call.accountID, "acct_123")
	}
	if call.quantity != 1 {
		t.Fatalf("quantity = %d, want 1", call.quantity)
	}
	expectedUnitPrice := costUSDToMicrocents(2)
	if call.unitPrice != expectedUnitPrice {
		t.Fatalf("unitPrice = %d, want %d", call.unitPrice.Microcents(), expectedUnitPrice.Microcents())
	}
}

func TestAccountingTransport_BillingBacked_FullOverageChargesFullCost(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	userID := "billing-full-overage-user"
	createTestUser(t, db, userID, "billing-full-overage@example.com")

	now := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	data := &billingGatewayDataSpy{
		DBGatewayData: &DBGatewayData{DB: db},
		remaining:     tender.Mint(2, 0),
	}
	creditMgr, transport := newBillingBackedTransport(t, data, userID, now)

	ctx := context.Background()
	if _, err := creditMgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("failed to initialize credit: %v", err)
	}
	if _, err := creditMgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD); err != nil {
		t.Fatalf("failed to deplete free credit: %v", err)
	}

	remaining := transport.debitResponseCredits(1.25, false)
	if !floatClose(remaining, -1.25, 0.000001) {
		t.Fatalf("remaining = %f, want -1.25", remaining)
	}

	calls := data.useCreditsCalls()
	if len(calls) != 1 {
		t.Fatalf("UseCredits calls = %d, want 1", len(calls))
	}
	expectedUnitPrice := costUSDToMicrocents(1.25)
	if calls[0].unitPrice != expectedUnitPrice {
		t.Fatalf("unitPrice = %d, want %d", calls[0].unitPrice.Microcents(), expectedUnitPrice.Microcents())
	}
}

func TestAccountingTransport_BillingBacked_UseCreditsErrorKeepsGatewayDebit(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	userID := "billing-usecredits-error-user"
	createTestUser(t, db, userID, "billing-usecredits-error@example.com")

	now := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	data := &billingGatewayDataSpy{
		DBGatewayData: &DBGatewayData{DB: db},
		useCreditsErr: context.DeadlineExceeded,
	}
	creditMgr, transport := newBillingBackedTransport(t, data, userID, now)

	ctx := context.Background()
	if _, err := creditMgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("failed to initialize credit: %v", err)
	}
	if _, err := creditMgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD); err != nil {
		t.Fatalf("failed to deplete free credit: %v", err)
	}

	remaining := transport.debitResponseCredits(1, false)
	if !floatClose(remaining, -1, 0.000001) {
		t.Fatalf("remaining = %f, want -1", remaining)
	}

	calls := data.useCreditsCalls()
	if len(calls) != 1 {
		t.Fatalf("UseCredits calls = %d, want 1", len(calls))
	}
}

func TestAccountingTransport_BillingBacked_ConcurrentOverageCharging(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	defer db.Close()

	userID := "billing-concurrent-overage-user"
	createTestUser(t, db, userID, "billing-concurrent-overage@example.com")

	now := time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC)
	data := &billingGatewayDataSpy{
		DBGatewayData: &DBGatewayData{DB: db},
		remaining:     tender.Mint(3, 0),
	}
	creditMgr, transport := newBillingBackedTransport(t, data, userID, now)

	ctx := context.Background()
	if _, err := creditMgr.CheckAndRefreshCredit(ctx, userID); err != nil {
		t.Fatalf("failed to initialize credit: %v", err)
	}
	if _, err := creditMgr.DebitCredit(ctx, userID, initialFreeCreditNoSubscriptionUSD-20); err != nil {
		t.Fatalf("failed to prepare concurrent overage balance: %v", err)
	}

	const (
		requests = 10
		costUSD  = 3.0
	)
	var wg sync.WaitGroup
	wg.Add(requests)
	for range requests {
		go func() {
			defer wg.Done()
			_ = transport.debitResponseCredits(costUSD, false)
		}()
	}
	wg.Wait()

	info, err := creditMgr.CheckAndRefreshCredit(ctx, userID)
	if err != ErrInsufficientCredit {
		t.Fatalf("CheckAndRefreshCredit error = %v, want %v", err, ErrInsufficientCredit)
	}
	if !floatClose(info.Available, -10, 0.000001) {
		t.Fatalf("final available = %f, want -10", info.Available)
	}

	calls := data.useCreditsCalls()
	if len(calls) != 4 {
		t.Fatalf("UseCredits calls = %d, want 4", len(calls))
	}

	var billedMicrocents int64
	for _, call := range calls {
		if call.accountID != "acct_123" {
			t.Fatalf("accountID = %q, want %q", call.accountID, "acct_123")
		}
		if call.quantity != 1 {
			t.Fatalf("quantity = %d, want 1", call.quantity)
		}
		billedMicrocents += call.unitPrice.Microcents()
	}
	if billedMicrocents != 10_000_000 {
		t.Fatalf("billed microcents = %d, want %d", billedMicrocents, 10_000_000)
	}
}
