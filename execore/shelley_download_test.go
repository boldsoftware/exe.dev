package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleShelleyDownload(t *testing.T) {
	s := newTestServer(t)

	tests := []struct {
		name           string
		archParam      string
		wantStatus     int
		wantHeader     string
		wantBodyString string
	}{
		{
			name:           "no arch parameter shows HTML page",
			archParam:      "",
			wantStatus:     http.StatusOK,
			wantHeader:     "text/html",
			wantBodyString: "Download Shelley",
		},
		{
			name:       "linux-amd64 downloads binary",
			archParam:  "linux-amd64",
			wantStatus: http.StatusOK,
			wantHeader: "application/octet-stream",
		},
		{
			name:       "amd64 downloads binary",
			archParam:  "amd64",
			wantStatus: http.StatusOK,
			wantHeader: "application/octet-stream",
		},
		{
			name:       "linux-arm64 downloads binary",
			archParam:  "linux-arm64",
			wantStatus: http.StatusOK,
			wantHeader: "application/octet-stream",
		},
		{
			name:       "arm64 downloads binary",
			archParam:  "arm64",
			wantStatus: http.StatusOK,
			wantHeader: "application/octet-stream",
		},
		{
			name:           "unsupported arch returns error",
			archParam:      "windows-amd64",
			wantStatus:     http.StatusBadRequest,
			wantBodyString: "Unsupported architecture",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/shelley/download"
			if tt.archParam != "" {
				url += "?arch=" + tt.archParam
			}

			req := httptest.NewRequest("GET", url, nil)
			req.Host = s.env.WebHost
			w := httptest.NewRecorder()

			s.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantHeader != "" {
				contentType := w.Header().Get("Content-Type")
				if !strings.Contains(contentType, tt.wantHeader) {
					t.Errorf("got Content-Type %q, want it to contain %q", contentType, tt.wantHeader)
				}
			}

			if tt.wantBodyString != "" {
				body := w.Body.String()
				if !strings.Contains(body, tt.wantBodyString) {
					t.Errorf("body doesn't contain %q, got: %s", tt.wantBodyString, body)
				}
			}

			// For successful binary downloads, verify Content-Disposition header
			if tt.wantStatus == http.StatusOK && tt.wantHeader == "application/octet-stream" {
				disposition := w.Header().Get("Content-Disposition")
				if !strings.HasPrefix(disposition, "attachment; filename=shelley-") {
					t.Errorf("got Content-Disposition %q, want it to start with 'attachment; filename=shelley-'", disposition)
				}
			}
		})
	}
}

func TestRenderShelleyDownloadPage(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest("GET", "/shelley/download", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check for key elements in the HTML
	wantStrings := []string{
		"Download Shelley",
		"linux-amd64",
		"linux-arm64",
		"/shelley/download?arch=linux-amd64",
		"/shelley/download?arch=linux-arm64",
		"Intel and AMD processors",
		"ARM processors",
	}

	for _, want := range wantStrings {
		if !strings.Contains(body, want) {
			t.Errorf("HTML body missing %q", want)
		}
	}
}
