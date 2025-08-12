package exe

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEmbeddedFiles(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":0", "", ":0", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	tests := []struct {
		name         string
		path         string
		expectedCode int
		contentType  string
		contains     []string
	}{
		{
			name:         "root serves welcome.html",
			path:         "/",
			expectedCode: http.StatusOK,
			contentType:  "text/html",
			contains:     []string{"exe.dev", "just use ssh", "<!DOCTYPE html>"},
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

func TestEmbeddedSizes(t *testing.T) {
	// Verify that embedded files are not empty and have reasonable sizes
	if len(welcomeHTML) == 0 {
		t.Error("welcomeHTML is empty")
	}

	if len(exeDevPNG) == 0 {
		t.Error("exeDevPNG is empty")
	}

	if len(browserWoodcutPNG) == 0 {
		t.Error("browserWoodcutPNG is empty")
	}

	// Check that welcome.html has reasonable size (at least 1KB)
	if len(welcomeHTML) < 1024 {
		t.Errorf("welcomeHTML seems too small: %d bytes", len(welcomeHTML))
	}

	// Check that PNGs have reasonable sizes (at least 10KB for real images)
	if len(exeDevPNG) < 10240 {
		t.Errorf("exeDevPNG seems too small: %d bytes", len(exeDevPNG))
	}

	if len(browserWoodcutPNG) < 10240 {
		t.Errorf("browserWoodcutPNG seems too small: %d bytes", len(browserWoodcutPNG))
	}

	t.Logf("Embedded file sizes:")
	t.Logf("  welcome.html: %d bytes", len(welcomeHTML))
	t.Logf("  exe.dev.png: %d bytes", len(exeDevPNG))
	t.Logf("  browser-woodcut.png: %d bytes", len(browserWoodcutPNG))
}
