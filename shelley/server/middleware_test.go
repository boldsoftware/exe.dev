package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireHeaderMiddleware_BlocksAPIWithoutHeader(t *testing.T) {
	handler := RequireHeaderMiddleware("X-Exedev-Userid")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/conversations", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for API request without required header, got %d", w.Code)
	}
}

func TestRequireHeaderMiddleware_AllowsAPIWithHeader(t *testing.T) {
	handler := RequireHeaderMiddleware("X-Exedev-Userid")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/conversations", nil)
	req.Header.Set("X-Exedev-Userid", "user123")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for API request with required header, got %d", w.Code)
	}
}

func TestRequireHeaderMiddleware_AllowsNonAPIWithoutHeader(t *testing.T) {
	handler := RequireHeaderMiddleware("X-Exedev-Userid")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for non-API request without required header, got %d", w.Code)
	}
}

func TestRequireHeaderMiddleware_AllowsVersionEndpointWithoutHeader(t *testing.T) {
	handler := RequireHeaderMiddleware("X-Exedev-Userid")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for /version without required header, got %d", w.Code)
	}
}

func TestGzipHandler_CompressesResponse(t *testing.T) {
	handler := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message": "hello world"}`))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding: gzip, got %q", w.Header().Get("Content-Encoding"))
	}

	// Verify we can decompress the response
	gr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gr.Close()

	body, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("failed to read gzip body: %v", err)
	}

	if !bytes.Contains(body, []byte("hello world")) {
		t.Errorf("decompressed body doesn't contain expected content: %s", body)
	}
}

func TestGzipHandler_SkipsWhenNoAcceptEncoding(t *testing.T) {
	handler := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message": "hello"}`))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	// No Accept-Encoding header
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Header().Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding, got %q", w.Header().Get("Content-Encoding"))
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("hello")) {
		t.Errorf("body doesn't contain expected content: %s", w.Body.String())
	}
}
