package llmgateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"exe.dev/accounting"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// mockTransport implements http.RoundTripper for testing
type mockTransport struct {
	response *http.Response
	err      error
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

// createTestDB creates a temporary database for testing
func createTestDB(t *testing.T) (*sqlite.DB, func()) {
	tmpFile, err := os.CreateTemp("", "accounting-transport-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	// Run migrations
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open database: %v", err)
	}

	if err := exedb.RunMigrations(rawDB); err != nil {
		rawDB.Close()
		os.Remove(dbPath)
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

	// Open with sqlite wrapper
	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open sqlite database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(dbPath)
	}

	return db, cleanup
}

// Factory function to create an accountingTransport for testing
func newTestAccountingTransport(balance float64, balanceErr error, rt http.RoundTripper, billingAccountID string) (*accountingTransport, *accounting.Accountant, *sqlite.DB) {
	db, _ := createTestDB(&testing.T{})
	accountant := accounting.NewAccountant()

	// If we want a balance, add credits
	if balance > 0 {
		credit := accounting.UsageCredit{
			BillingAccountID: billingAccountID,
			Amount:           balance,
			PaymentMethod:    "test",
			PaymentID:        "test-payment",
			Status:           "completed",
		}
		_ = db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			return accountant.CreditUsage(ctx, tx, credit)
		})
	}

	return &accountingTransport{
		RoundTripper:     rt,
		accountant:       accountant,
		db:               db,
		billingAccountID: billingAccountID,
		baseURL:          "https://example.com",
		apiType:          "anthropic", // Default to anthropic for tests
		testDebitDone:    make(chan bool, 1),
	}, accountant, db
}

// createGzippedJSONResponse creates a gzipped JSON response body
func createGzippedJSONResponse(t *testing.T, data any) io.ReadCloser {
	jsonData, err := json.Marshal(data)
	require.NoError(t, err)

	var b bytes.Buffer
	gzWriter := gzip.NewWriter(&b)
	_, err = gzWriter.Write(jsonData)
	require.NoError(t, err)
	require.NoError(t, gzWriter.Close())

	return io.NopCloser(bytes.NewReader(b.Bytes()))
}

// TestAccountingTransportCheckCredits tests the checkCredits method
func TestAccountingTransportCheckCredits(t *testing.T) {
	tests := []struct {
		name    string
		balance float64
		wantErr bool
	}{
		{
			name:    "sufficient balance",
			balance: 10.0,
			wantErr: false,
		},
		{
			name:    "insufficient balance",
			balance: 0.0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test accountingTransport with our accountant
			at, act, db := newTestAccountingTransport(tt.balance, nil, nil, tt.name)
			defer db.Close()

			// Test the checkCredits method
			err := at.checkCredits(context.Background(), tt.name)
			var bal float64
			balErr := db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
				var err error
				bal, err = act.GetBalance(ctx, rx, tt.name)
				return err
			})
			t.Logf("%s: biling id %q, tt.balance %f, bal: %f, balErr: %v", at.billingAccountID, tt.name, tt.balance, bal, balErr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}

	// Test that insufficient balance returns correct error message
	t.Run("insufficient balance error message", func(t *testing.T) {
		balance := 0.0
		baseURL := "https://example.com"
		BillingAccountID := "123"

		// Get error from test implementation
		testTransport, _, db := newTestAccountingTransport(balance, nil, nil, BillingAccountID)
		defer db.Close()
		testErr := testTransport.checkCredits(context.Background(), BillingAccountID)

		assert.Error(t, testErr)
		assert.Contains(t, testErr.Error(), baseURL+"/buy")
		assert.Contains(t, testErr.Error(), "insufficient")
	})
}

// TestAccountingTransportRoundTrip tests the RoundTrip method
func TestAccountingTransportRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		balance      float64
		expectedCode int
	}{
		{
			name:         "sufficient balance",
			balance:      10.0,
			expectedCode: http.StatusOK,
		},
		{
			name:         "insufficient balance",
			balance:      0.0,
			expectedCode: http.StatusPaymentRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock response
			mockResp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("test response")),
				Header:     make(http.Header),
			}

			// Create a mock RoundTripper
			mockRT := &mockTransport{response: mockResp}

			// Create an actual accountingTransport with our accountant
			at, _, db := newTestAccountingTransport(tt.balance, nil, mockRT, "123")
			defer db.Close()

			// Create a test request
			req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)

			// Test the RoundTrip method
			resp, err := at.RoundTrip(req)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCode, resp.StatusCode)
		})
	}
}

// TestModifyResponseAnthropic tests the modifyResponse method with Anthropic responses
func TestModifyResponseAnthropic(t *testing.T) {
	// Create test response data
	respData := &anthropicResponseUsageInfo{
		ID:    "msg_test123",
		Model: "claude-3-haiku-20240307",
		Usage: &accounting.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	// Create JSON response
	jsonData, err := json.Marshal(respData)
	require.NoError(t, err)

	// Create HTTP response
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(jsonData)),
		Header:     make(http.Header),
	}
	resp.Header.Add("Content-type", "application/json")

	// Create accounting transport
	at, act, db := newTestAccountingTransport(100.0, nil, nil, "123")
	defer db.Close()
	at.apiType = "anthropic"

	// Test modifyResponse
	err = at.modifyResponse(resp)
	assert.NoError(t, err)

	// Verify response body is still readable
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, jsonData, body, "Response body should be preserved")

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify the balance was debited
	var balance float64
	err = db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		var err error
		balance, err = act.GetBalance(ctx, rx, "123")
		return err
	})
	assert.NoError(t, err)
	assert.Less(t, balance, 100.0, "Balance should have decreased from the debit")
}

// TestModifyResponseGzipped tests modifyResponse with gzipped content
func TestModifyResponseGzipped(t *testing.T) {
	// Create test response data
	respData := &anthropicResponseUsageInfo{
		ID:    "msg_gzip_test",
		Model: "claude-3-sonnet-20240229",
		Usage: &accounting.Usage{
			InputTokens:  200,
			OutputTokens: 100,
		},
	}

	// Create gzipped JSON response
	responseBody := createGzippedJSONResponse(t, respData)

	// Create HTTP response with gzip encoding
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       responseBody,
		Header:     make(http.Header),
	}
	resp.Header.Set("Content-Encoding", "gzip")
	resp.Header.Add("Content-type", "application/json")
	// Create accounting transport
	at, act, db := newTestAccountingTransport(100.0, nil, nil, "123")
	defer db.Close()
	at.apiType = "anthropic"

	// Test modifyResponse
	err := at.modifyResponse(resp)
	assert.NoError(t, err)

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify the balance was debited
	var balance float64
	err = db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		var err error
		balance, err = act.GetBalance(ctx, rx, "123")
		return err
	})
	assert.NoError(t, err)
	assert.Less(t, balance, 100.0, "Balance should have decreased from the debit")
}

// TestModifyResponseErrorCases tests error handling in modifyResponse
func TestModifyResponseErrorCases(t *testing.T) {
	t.Run("non-200 status code", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewReader([]byte("error"))),
			Header:     make(http.Header),
		}

		at, _, db := newTestAccountingTransport(100.0, nil, nil, "123")
		defer db.Close()
		err := at.modifyResponse(resp)
		assert.NoError(t, err, "Should not error for non-200 responses")

		// Should not add cost header
		costHeader := resp.Header.Get("Skaband-Cost-Microcents")
		assert.Empty(t, costHeader, "Should not set cost header for non-200 responses")
	})

	t.Run("malformed JSON", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte("invalid json{"))),
			Header:     make(http.Header),
		}
		resp.Header.Add("Content-type", "application/json")

		at, _, db := newTestAccountingTransport(100.0, nil, nil, "123")
		defer db.Close()
		at.apiType = "anthropic"
		err := at.modifyResponse(resp)
		assert.Error(t, err, "Should error on malformed JSON")
		assert.Contains(t, err.Error(), "json decode error")
	})
}
