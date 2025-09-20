package exe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedFiles(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	tests := []struct {
		name         string
		path         string
		expectedCode int
		contentType  string
		contains     []string
	}{
		{
			name:         "root redirects to /soon",
			path:         "/",
			expectedCode: http.StatusTemporaryRedirect,
			contentType:  "text/html",
			contains:     []string{},
		},
		{
			name:         "/soon serves comingsoon.html",
			path:         "/soon",
			expectedCode: http.StatusOK,
			contentType:  "text/html",
			contains:     []string{"coming soon", "<!DOCTYPE html>"},
		},
		{
			name:         "favicon.ico is served",
			path:         "/favicon.ico",
			expectedCode: http.StatusOK,
			contentType:  "image/",   // Accept any image content type for ico files
			contains:     []string{}, // ICO is binary, checking content-type is enough
		},
		{
			name:         "exe.dev.png is served",
			path:         "/exe.dev.png",
			expectedCode: http.StatusOK,
			contentType:  "image/png",
			contains:     []string{}, // PNG is binary, checking content-type is enough
		},
		{
			name:         "browser-woodcut.png is served",
			path:         "/browser-woodcut.png",
			expectedCode: http.StatusOK,
			contentType:  "image/png",
			contains:     []string{}, // PNG is binary, checking content-type is enough
		},
		{
			name:         "non-existent path returns 404",
			path:         "/does-not-exist",
			expectedCode: http.StatusNotFound,
			contentType:  "",
			contains:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			// Call the handler
			server.ServeHTTP(w, req)

			// Check status code
			if w.Code != tt.expectedCode {
				t.Errorf("Expected status code %d, got %d", tt.expectedCode, w.Code)
			}

			// Check content type
			if tt.contentType != "" {
				contentType := w.Header().Get("Content-Type")
				if !strings.Contains(contentType, tt.contentType) {
					t.Errorf("Expected Content-Type to contain %q, got %q", tt.contentType, contentType)
				}
			}

			// Check response body contains expected strings
			body := w.Body.String()
			for _, expected := range tt.contains {
				if !strings.Contains(body, expected) {
					t.Errorf("Expected response to contain %q, but it didn't. Response length: %d", expected, len(body))
				}
			}

			// For image files, just verify they're not empty
			if strings.HasSuffix(tt.path, ".png") && tt.expectedCode == http.StatusOK {
				if len(body) == 0 {
					t.Errorf("Image %s is empty", tt.path)
				}
				// PNG files should start with PNG magic bytes
				if len(body) >= 8 {
					pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
					for i, b := range pngMagic {
						if body[i] != b {
							t.Errorf("Image %s doesn't have valid PNG header", tt.path)
							break
						}
					}
				}
			}
		})
	}
}
