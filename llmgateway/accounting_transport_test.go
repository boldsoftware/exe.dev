package llmgateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/accounting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// Factory function to create an accountingTransport for testing
func newTestAccountingTransport(balance float64, balanceErr error, rt http.RoundTripper, billingAccountID string) (*accountingTransport, *mockAccountant) {
	mockAcct := &mockAccountant{
		balances: map[string]float64{
			billingAccountID: balance,
		},
	}
	if balanceErr != nil {
		mockAcct.balanceErrors = map[string]error{
			billingAccountID: balanceErr,
		}
	}
	return &accountingTransport{
		RoundTripper:     rt,
		accountant:       mockAcct,
		billingAccountID: billingAccountID,
		baseURL:          "https://example.com",
		apiType:          "anthropic", // Default to anthropic for tests
		testDebitDone:    make(chan bool, 1),
	}, mockAcct
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
		name       string
		balance    float64
		balanceErr error
		wantErr    bool
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
		{
			name:       "balance check error",
			balance:    0.0,
			balanceErr: fmt.Errorf("database error"),
			wantErr:    false, // Should not error, just log
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test accountingTransport with our mock
			at, act := newTestAccountingTransport(tt.balance, tt.balanceErr, nil, tt.name)

			// Test the checkCredits method
			err := at.checkCredits(context.Background(), tt.name)
			bal, balErr := act.GetBalance(context.Background(), tt.name)
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
		testTransport, _ := newTestAccountingTransport(balance, nil, nil, BillingAccountID)
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
		balanceErr   error
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
		{
			name:         "balance check error - allows request",
			balance:      0.0,
			balanceErr:   fmt.Errorf("database error"),
			expectedCode: http.StatusOK,
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

			// Create an actual accountingTransport with our mocks
			at, _ := newTestAccountingTransport(tt.balance, tt.balanceErr, mockRT, "123")

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
	at, mockAcct := newTestAccountingTransport(100.0, nil, nil, "123")
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

	// Verify accounting was called
	assert.Len(t, mockAcct.debits, 1, "Should have recorded one usage debit")
	debit := mockAcct.debits[0]
	assert.Equal(t, "msg_test123", debit.MessageID)
	assert.Equal(t, "claude-3-haiku-20240307", debit.Model)
	assert.Equal(t, uint64(100), debit.Usage.InputTokens)
	assert.Equal(t, uint64(50), debit.Usage.OutputTokens)
	assert.Greater(t, debit.Usage.CostUSD, 0.0, "Cost should be calculated")
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
	at, mockAcct := newTestAccountingTransport(100.0, nil, nil, "123")
	at.apiType = "anthropic"

	// Test modifyResponse
	err := at.modifyResponse(resp)
	assert.NoError(t, err)

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify accounting was called
	assert.Len(t, mockAcct.debits, 1, "Should have recorded one usage debit")
	debit := mockAcct.debits[0]
	assert.Equal(t, "msg_gzip_test", debit.MessageID)
	assert.Equal(t, "claude-3-sonnet-20240229", debit.Model)
}

// TestModifyResponseErrorCases tests error handling in modifyResponse
func TestModifyResponseErrorCases(t *testing.T) {
	t.Run("non-200 status code", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewReader([]byte("error"))),
			Header:     make(http.Header),
		}

		at, _ := newTestAccountingTransport(100.0, nil, nil, "123")
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

		at, _ := newTestAccountingTransport(100.0, nil, nil, "123")
		at.apiType = "anthropic"
		err := at.modifyResponse(resp)
		assert.Error(t, err, "Should error on malformed JSON")
		assert.Contains(t, err.Error(), "json decode error")
	})
}
