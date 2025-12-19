package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFMiddleware_BlocksPostWithoutHeader(t *testing.T) {
	handler := CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for POST without X-Shelley-Request, got %d", w.Code)
	}
}

func TestCSRFMiddleware_AllowsPostWithHeader(t *testing.T) {
	handler := CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/test", nil)
	req.Header.Set("X-Shelley-Request", "1")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for POST with X-Shelley-Request, got %d", w.Code)
	}
}

func TestCSRFMiddleware_AllowsGetWithoutHeader(t *testing.T) {
	handler := CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for GET without X-Shelley-Request, got %d", w.Code)
	}
}

func TestCSRFMiddleware_BlocksPutWithoutHeader(t *testing.T) {
	handler := CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("PUT", "/api/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for PUT without X-Shelley-Request, got %d", w.Code)
	}
}

func TestCSRFMiddleware_BlocksDeleteWithoutHeader(t *testing.T) {
	handler := CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("DELETE", "/api/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 for DELETE without X-Shelley-Request, got %d", w.Code)
	}
}

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
