package llmgateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/stage"
	"exe.dev/tslog"
)

type preflightGatewayData struct {
	creditInfo *CreditInfo
	creditErr  error

	accountID      string
	accountExists  bool
	accountErr     error
	accountLookups int

	useCreditsBalance tender.Value
	useCreditsErr     error
	useCreditsCalls   int
	useCreditsAccount string
	useCreditsQty     int
	useCreditsPrice   tender.Value
}

func (d *preflightGatewayData) BoxCreator(context.Context, string) (string, bool, error) {
	return "test-user", true, nil
}

func (d *preflightGatewayData) CheckAndRefreshCredit(context.Context, string, time.Time) (*CreditInfo, error) {
	return d.creditInfo, d.creditErr
}

func (d *preflightGatewayData) TopUpOnBillingUpgrade(context.Context, string, time.Time) error {
	return nil
}

func (d *preflightGatewayData) DebitCredit(context.Context, string, float64, time.Time) (*CreditInfo, error) {
	return nil, nil
}

func (d *preflightGatewayData) AccountIDForUser(context.Context, string) (string, bool, error) {
	d.accountLookups++
	return d.accountID, d.accountExists, d.accountErr
}

func (d *preflightGatewayData) TeamBillingAccountID(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (d *preflightGatewayData) UseCredits(_ context.Context, accountID string, quantity int, unitPrice tender.Value) (tender.Value, error) {
	d.useCreditsCalls++
	d.useCreditsAccount = accountID
	d.useCreditsQty = quantity
	d.useCreditsPrice = unitPrice
	return d.useCreditsBalance, d.useCreditsErr
}

func newPreflightTestGateway(t *testing.T, data GatewayData) *llmGateway {
	t.Helper()
	gateway := &llmGateway{
		now:     time.Now,
		data:    data,
		apiKeys: APIKeys{Anthropic: "test", OpenAI: "test", Fireworks: "test"},
		env:     stage.Test(),
		log:     tslog.Slogger(t),
	}
	gateway.creditMgr = NewCreditManager(data)
	return gateway
}

func newPreflightTestRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/_/gateway/unknown/v1/messages", nil)
	req.Header.Set("X-Exedev-Box", "test-box")
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func TestGateway_PreRequestCreditPreflight(t *testing.T) {
	creditExhausted := Plan{
		Name:                 "no_billing",
		CreditExhaustedError: "LLM credits exhausted",
	}

	tests := []struct {
		name                string
		data                *preflightGatewayData
		wantStatus          int
		wantBodyContains    string
		wantAccountLookups  int
		wantUseCreditsCalls int
	}{
		{
			name: "monthly free credit available no preflight UseCredits call",
			data: &preflightGatewayData{
				creditInfo: &CreditInfo{Available: 0.0001, Plan: planNoBilling},
			},
			wantStatus:          http.StatusNotFound,
			wantAccountLookups:  0,
			wantUseCreditsCalls: 0,
		},
		{
			name: "free exhausted with no billing account returns 402",
			data: &preflightGatewayData{
				creditInfo:    &CreditInfo{Available: 0, Plan: creditExhausted},
				accountExists: false,
			},
			wantStatus:          http.StatusPaymentRequired,
			wantBodyContains:    "LLM credits exhausted",
			wantAccountLookups:  1,
			wantUseCreditsCalls: 0,
		},
		{
			name: "free exhausted with positive billing balance allows request",
			data: &preflightGatewayData{
				creditInfo:        &CreditInfo{Available: 0, Plan: creditExhausted},
				accountID:         "acct_123",
				accountExists:     true,
				useCreditsBalance: tender.Mint(1, 0),
			},
			wantStatus:          http.StatusNotFound,
			wantAccountLookups:  1,
			wantUseCreditsCalls: 1,
		},
		{
			name: "free exhausted with zero billing balance returns 402",
			data: &preflightGatewayData{
				creditInfo:        &CreditInfo{Available: 0, Plan: creditExhausted},
				accountID:         "acct_123",
				accountExists:     true,
				useCreditsBalance: tender.Zero(),
			},
			wantStatus:          http.StatusPaymentRequired,
			wantBodyContains:    "LLM credits exhausted",
			wantAccountLookups:  1,
			wantUseCreditsCalls: 1,
		},
		{
			name: "free exhausted with preflight UseCredits error returns 500",
			data: &preflightGatewayData{
				creditInfo:        &CreditInfo{Available: 0, Plan: creditExhausted},
				accountID:         "acct_123",
				accountExists:     true,
				useCreditsBalance: tender.Zero(),
				useCreditsErr:     errors.New("billing unavailable"),
			},
			wantStatus:          http.StatusInternalServerError,
			wantBodyContains:    "failed to check billing credits",
			wantAccountLookups:  1,
			wantUseCreditsCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newPreflightTestGateway(t, tc.data)
			req := newPreflightTestRequest()
			rec := httptest.NewRecorder()

			gateway.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBodyContains != "" && !strings.Contains(rec.Body.String(), tc.wantBodyContains) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tc.wantBodyContains)
			}
			if tc.wantBodyContains != "" {
				if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
					t.Fatalf("content-type = %q, want %q", got, "text/plain; charset=utf-8")
				}
				if !strings.HasSuffix(rec.Body.String(), "\n") {
					t.Fatalf("body %q missing trailing newline from http.Error encoding", rec.Body.String())
				}
			}
			if tc.data.accountLookups != tc.wantAccountLookups {
				t.Fatalf("AccountIDForUser calls = %d, want %d", tc.data.accountLookups, tc.wantAccountLookups)
			}
			if tc.data.useCreditsCalls != tc.wantUseCreditsCalls {
				t.Fatalf("UseCredits calls = %d, want %d", tc.data.useCreditsCalls, tc.wantUseCreditsCalls)
			}
			if tc.wantUseCreditsCalls > 0 {
				if tc.data.useCreditsAccount != tc.data.accountID {
					t.Fatalf("UseCredits accountID = %q, want %q", tc.data.useCreditsAccount, tc.data.accountID)
				}
				if tc.data.useCreditsQty != 0 {
					t.Fatalf("UseCredits quantity = %d, want 0", tc.data.useCreditsQty)
				}
				if tc.data.useCreditsPrice != tender.Zero() {
					t.Fatalf("UseCredits unitPrice = %d, want %d", tc.data.useCreditsPrice.Microcents(), tender.Zero().Microcents())
				}
			}
		})
	}
}
