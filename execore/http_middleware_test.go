package execore

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoggerMiddlewareStatusCode(t *testing.T) {
	tests := []struct {
		name           string
		handlerFunc    http.HandlerFunc
		expectedStatus int
	}{
		{
			name: "explicit 200",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			},
			expectedStatus: 200,
		},
		{
			name: "implicit 200 from Write",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("ok"))
			},
			expectedStatus: 200,
		},
		{
			name: "404 not found",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			expectedStatus: 404,
		},
		{
			name: "500 internal error",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "internal error", http.StatusInternalServerError)
			},
			expectedStatus: 500,
		},
		{
			name: "redirect",
			handlerFunc: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			},
			expectedStatus: 307,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture log output
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			// Create handler with middleware
			handler := LoggerMiddleware(logger)(tt.handlerFunc)

			// Make request
			req := httptest.NewRequest("GET", "/test", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			// Parse log output
			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to parse log output: %v\nLog output: %s", err, buf.String())
			}

			// Check status in log (slog-http nests it under response.status)
			response, ok := logEntry["response"].(map[string]interface{})
			if !ok {
				t.Fatalf("response not found in log or wrong type. Log: %v", logEntry)
			}
			status, ok := response["status"].(float64)
			if !ok {
				t.Fatalf("status not found in response or wrong type. Log: %v", logEntry)
			}

			if int(status) != tt.expectedStatus {
				t.Errorf("expected status %d in log, got %d", tt.expectedStatus, int(status))
			}

			// Verify actual response status matches
			if w.Code != tt.expectedStatus {
				t.Errorf("expected response status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}
