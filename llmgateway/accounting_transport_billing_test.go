package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/llmpricing"
	"github.com/stretchr/testify/require"
)

type useCreditsCall struct {
	accountID string
	quantity  int
	unitPrice tender.Value
}

type gatewayDataUseCreditsSpy struct {
	remaining      tender.Value
	useCreditsErr  error
	useCreditsCall []useCreditsCall
}

func (s *gatewayDataUseCreditsSpy) BoxCreator(context.Context, string) (string, bool, error) {
	panic("unexpected call: BoxCreator")
}

func (s *gatewayDataUseCreditsSpy) CheckAndRefreshCredit(context.Context, string, time.Time) (*CreditInfo, error) {
	panic("unexpected call: CheckAndRefreshCredit")
}

func (s *gatewayDataUseCreditsSpy) TopUpOnBillingUpgrade(context.Context, string, time.Time) error {
	panic("unexpected call: TopUpOnBillingUpgrade")
}

func (s *gatewayDataUseCreditsSpy) DebitCredit(context.Context, string, float64, time.Time) (*CreditInfo, error) {
	panic("unexpected call: DebitCredit")
}

func (s *gatewayDataUseCreditsSpy) AccountIDForUser(context.Context, string) (string, bool, error) {
	panic("unexpected call: AccountIDForUser")
}

func (s *gatewayDataUseCreditsSpy) UseCredits(ctx context.Context, accountID string, quantity int, unitPrice tender.Value) (tender.Value, error) {
	s.useCreditsCall = append(s.useCreditsCall, useCreditsCall{
		accountID: accountID,
		quantity:  quantity,
		unitPrice: unitPrice,
	})
	if s.useCreditsErr != nil {
		return tender.Zero(), s.useCreditsErr
	}
	return s.remaining, nil
}

func parseLogLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}
	return entries
}

func findLogByMessage(t *testing.T, entries []map[string]any, message string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["msg"] == message {
			return entry
		}
	}
	t.Fatalf("log message %q not found", message)
	return nil
}

func TestAccountingTransport_BillingBackedPostResponseDeduction_UseCreditsArgsAndDebugLog(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	creditData := &gatewayDataUseCreditsSpy{
		remaining: tender.Mint(0, 4321),
	}
	transport := &accountingTransport{
		provider:         llmpricing.ProviderOpenAI,
		log:              logger,
		creditMgr:        &CreditManager{data: creditData, now: time.Now},
		incomingReq:      httptest.NewRequest("POST", "/v1/chat/completions", nil),
		boxName:          "box-a",
		userID:           "user-a",
		billingBacked:    true,
		billingAccountID: "acct_123",
	}

	// 1 prompt + 1 completion token for gpt-4o-mini costs 75 cents per 1M tokens.
	// This is $0.00000075, which rounds to -1 microcent when debited.
	response := []byte(`{
		"id": "chatcmpl_123",
		"model": "gpt-4o-mini",
		"usage": {
			"prompt_tokens": 1,
			"completion_tokens": 1,
			"total_tokens": 2
		}
	}`)

	costInfo, err := transport.processResponseData(response)
	require.NoError(t, err)
	require.NotNil(t, costInfo)

	expectedCostUSD := llmpricing.CalculateCost(
		llmpricing.ProviderOpenAI,
		"gpt-4o-mini",
		llmpricing.Usage{
			InputTokens:  1,
			OutputTokens: 1,
		},
	)
	require.InDelta(t, expectedCostUSD, costInfo.CostUSD, 1e-12)

	require.Len(t, creditData.useCreditsCall, 1)
	call := creditData.useCreditsCall[0]
	require.Equal(t, "acct_123", call.accountID)
	require.Equal(t, 1, call.quantity)

	expectedUnitPrice := costUSDToNegativeMicrocents(expectedCostUSD)
	require.Equal(t, expectedUnitPrice, call.unitPrice)
	require.EqualValues(t, -1, call.unitPrice.Microcents())

	logEntries := parseLogLines(t, logs.String())
	debitLog := findLogByMessage(t, logEntries, "debited billing credits")
	require.Equal(t, "DEBUG", debitLog["level"])
	require.Equal(t, "acct_123", debitLog["account_id"])
	require.EqualValues(t, expectedUnitPrice.Microcents(), debitLog["unit_price_microcents"])
	require.EqualValues(t, creditData.remaining.Microcents(), debitLog["remaining_microcents"])
}

func TestAccountingTransport_BillingBackedPostResponseDeduction_UseCreditsErrorStillSucceeds(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	creditData := &gatewayDataUseCreditsSpy{
		useCreditsErr: errors.New("billing outage"),
	}
	transport := &accountingTransport{
		provider:         llmpricing.ProviderOpenAI,
		log:              logger,
		creditMgr:        &CreditManager{data: creditData, now: time.Now},
		incomingReq:      httptest.NewRequest("POST", "/v1/chat/completions", nil),
		boxName:          "box-a",
		userID:           "user-a",
		billingBacked:    true,
		billingAccountID: "acct_123",
	}

	response := []byte(`{
		"id": "chatcmpl_456",
		"model": "gpt-4o-mini",
		"usage": {
			"prompt_tokens": 1,
			"completion_tokens": 1,
			"total_tokens": 2
		}
	}`)

	costInfo, err := transport.processResponseData(response)
	require.NoError(t, err, "post-response processing should not fail when UseCredits fails")
	require.NotNil(t, costInfo)

	expectedCostUSD := llmpricing.CalculateCost(
		llmpricing.ProviderOpenAI,
		"gpt-4o-mini",
		llmpricing.Usage{
			InputTokens:  1,
			OutputTokens: 1,
		},
	)
	require.InDelta(t, expectedCostUSD, costInfo.CostUSD, 1e-12)

	require.Len(t, creditData.useCreditsCall, 1)
	call := creditData.useCreditsCall[0]
	require.Equal(t, "acct_123", call.accountID)
	require.Equal(t, 1, call.quantity)
	require.Equal(t, costUSDToNegativeMicrocents(expectedCostUSD), call.unitPrice)

	logEntries := parseLogLines(t, logs.String())
	errorLog := findLogByMessage(t, logEntries, "failed to debit billing credits")
	require.Equal(t, "ERROR", errorLog["level"])
	require.Equal(t, "acct_123", errorLog["account_id"])
	require.Equal(t, "billing outage", errorLog["error"])
}
