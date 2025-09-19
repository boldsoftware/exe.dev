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
	"strconv"
	"testing"

	"exe.dev/accounting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccountant implements Accountant for testing
type mockAccountant struct {
	balance      float64
	balanceErr   error
	usageDebits  []accounting.UsageDebit
	usageCredits []accounting.UsageCredit
}

// BillingAccountForBox implements accounting.Accountant.
func (m *mockAccountant) BillingAccountForBox(ctx context.Context, boxName string) (string, error) {
	panic("unimplemented")
}

// ApplyNewUserCredits implements accountant.
func (m *mockAccountant) ApplyNewUserCredits(ctx context.Context, billingAccountID string) any {
	panic("unimplemented")
}

// HasNewUserCredits implements accountant.
func (m *mockAccountant) HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any) {
	panic("unimplemented")
}

var _ accounting.Accountant = &mockAccountant{}

func (m *mockAccountant) GetUserBalance(ctx context.Context, BillingAccountID string) (float64, error) {
	if m.balanceErr != nil || m.balance != 0 {
		return m.balance, m.balanceErr
	}
	totalCredits := 0.0
	totalDebits := 0.0

	for _, credit := range m.usageCredits {
		totalCredits += credit.Amount
	}
	for _, debit := range m.usageDebits {
		totalDebits += debit.CostUSD
	}
	return totalCredits - totalDebits, nil
}

func (m *mockAccountant) DebitUsage(ctx context.Context, debit accounting.UsageDebit) error {
	m.usageDebits = append(m.usageDebits, debit)
	return nil
}

// CreditUsage implements accountant.
func (m *mockAccountant) CreditUsage(ctx context.Context, credit accounting.UsageCredit) error {
	m.usageCredits = append(m.usageCredits, credit)
	return nil
}

func (m *mockAccountant) AnthropicAPIKey() string {
	return "test-key"
}

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
func newTestAccountingTransport(balance float64, balanceErr error, rt http.RoundTripper, BillingAccountID string) (*accountingTransport, *mockAccountant) {
	mockAcct := &mockAccountant{
		balance:    balance,
		balanceErr: balanceErr,
	}

	return &accountingTransport{
		RoundTripper:     rt,
		accountant:       mockAcct,
		BillingAccountID: BillingAccountID,
		baseURL:          "https://example.com",
		apiType:          "antmsgs", // Default to antmsgs for tests
		testDebitDone:    make(chan bool),
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
			at, _ := newTestAccountingTransport(tt.balance, tt.balanceErr, nil, "123")

			// Test the checkCredits method
			err := at.checkCredits(context.Background(), "123")

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
		Usage: accounting.Usage{
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

	// Create accounting transport
	at, mockAcct := newTestAccountingTransport(100.0, nil, nil, "123")
	at.apiType = "antmsgs"

	// Test modifyResponse
	err = at.modifyResponse(resp)
	assert.NoError(t, err)

	// Verify the cost header was added
	costHeader := resp.Header.Get("Skaband-Cost-Microcents")
	assert.NotEmpty(t, costHeader, "Cost header should be set")

	// Parse the cost value
	costMicrocents, err := strconv.ParseUint(costHeader, 10, 64)
	assert.NoError(t, err)
	assert.Greater(t, costMicrocents, uint64(0), "Cost should be greater than 0")

	// Verify response body is still readable
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, jsonData, body, "Response body should be preserved")

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify accounting was called
	assert.Len(t, mockAcct.usageDebits, 1, "Should have recorded one usage debit")
	debit := mockAcct.usageDebits[0]
	assert.Equal(t, "msg_test123", debit.MessageID)
	assert.Equal(t, "claude-3-haiku-20240307", debit.Model)
	assert.Equal(t, uint64(100), debit.Usage.InputTokens)
	assert.Equal(t, uint64(50), debit.Usage.OutputTokens)
	assert.Greater(t, debit.Usage.CostUSD, 0.0, "Cost should be calculated")
}

// TestModifyResponseGemini tests the modifyResponse method with Gemini responses
func TestModifyResponseGemini(t *testing.T) {
	// Create test Gemini response data
	respData := &geminiResponseUsageInfo{
		UsageMetadata: struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
		}{
			PromptTokenCount:     80,
			CandidatesTokenCount: 40,
			TotalTokenCount:      120,
		},
		Candidates:   []any{map[string]string{"content": "test response"}},
		ModelVersion: "gemini-1.5-pro-001",
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

	// Create accounting transport for Gemini
	at, mockAcct := newTestAccountingTransport(100.0, nil, nil, "123")
	at.apiType = "gemmsgs"

	// Test modifyResponse
	err = at.modifyResponse(resp)
	assert.NoError(t, err)

	// Verify the cost header was added
	costHeader := resp.Header.Get("Skaband-Cost-Microcents")
	assert.NotEmpty(t, costHeader, "Cost header should be set")

	// Parse the cost value
	costMicrocents, err := strconv.ParseUint(costHeader, 10, 64)
	assert.NoError(t, err)
	assert.Greater(t, costMicrocents, uint64(0), "Cost should be greater than 0")

	// Verify response body is still readable
	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, jsonData, body, "Response body should be preserved")

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify accounting was called
	assert.Len(t, mockAcct.usageDebits, 1, "Should have recorded one usage debit")
	debit := mockAcct.usageDebits[0]
	assert.Equal(t, "gemini-1.5-pro", debit.Model) // Should map to pricing model
	assert.Equal(t, uint64(80), debit.Usage.InputTokens)
	assert.Equal(t, uint64(40), debit.Usage.OutputTokens)
	assert.Greater(t, debit.Usage.CostUSD, 0.0, "Cost should be calculated")
}

// TestModifyResponseGzipped tests modifyResponse with gzipped content
func TestModifyResponseGzipped(t *testing.T) {
	// Create test response data
	respData := &anthropicResponseUsageInfo{
		ID:    "msg_gzip_test",
		Model: "claude-3-sonnet-20240229",
		Usage: accounting.Usage{
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

	// Create accounting transport
	at, mockAcct := newTestAccountingTransport(100.0, nil, nil, "123")
	at.apiType = "antmsgs"

	// Test modifyResponse
	err := at.modifyResponse(resp)
	assert.NoError(t, err)

	// Verify the cost header was added
	costHeader := resp.Header.Get("Skaband-Cost-Microcents")
	assert.NotEmpty(t, costHeader, "Cost header should be set")

	// Wait for async accounting to complete
	<-at.testDebitDone

	// Verify accounting was called
	assert.Len(t, mockAcct.usageDebits, 1, "Should have recorded one usage debit")
	debit := mockAcct.usageDebits[0]
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

		at, _ := newTestAccountingTransport(100.0, nil, nil, "123")
		at.apiType = "antmsgs"
		err := at.modifyResponse(resp)
		assert.Error(t, err, "Should error on malformed JSON")
		assert.Contains(t, err.Error(), "json decode error")
	})

	t.Run("empty Gemini response", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte(""))),
			Header:     make(http.Header),
		}

		at, _ := newTestAccountingTransport(100.0, nil, nil, "123")
		at.apiType = "gemmsgs"
		err := at.modifyResponse(resp)
		assert.Error(t, err, "Should error on empty Gemini response")
		assert.Contains(t, err.Error(), "empty gemini response")
	})

	t.Run("unknown API type", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"test":"data"}`))),
			Header:     make(http.Header),
		}

		at, _ := newTestAccountingTransport(100.0, nil, nil, "123")
		at.apiType = "unknown"
		err := at.modifyResponse(resp)
		assert.Error(t, err, "Should error on unknown API type")
		assert.Contains(t, err.Error(), "unknown API type")
	})
}
