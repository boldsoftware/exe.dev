package exeweb

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHSTSMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := HSTSMiddleware(inner)

	t.Run("sets header for TLS request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		got := rec.Header().Get("Strict-Transport-Security")
		want := "max-age=63072000; includeSubDomains; preload"
		if got != want {
			t.Errorf("Strict-Transport-Security = %q, want %q", got, want)
		}
	})

	t.Run("no header for non-TLS request", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("Strict-Transport-Security = %q, want empty", got)
		}
	})
}
