package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenAuth(t *testing.T) {
	handler := TokenAuth("secret-token")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"valid token", "Bearer secret-token", http.StatusOK},
		{"invalid token", "Bearer wrong-token", http.StatusUnauthorized},
		{"missing header", "", http.StatusUnauthorized},
		{"no bearer prefix", "secret-token", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/report", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
