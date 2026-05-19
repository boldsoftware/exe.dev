package flue

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		method         string
		path           string
		wantHandled    bool
		wantStatus     int
		wantCT         string
		wantBodySubstr string
	}{
		{
			name:           "md served as text/markdown",
			method:         http.MethodGet,
			path:           "/flue/exedev.md",
			wantHandled:    true,
			wantStatus:     http.StatusOK,
			wantCT:         "text/markdown; charset=utf-8",
			wantBodySubstr: "# Add a Flue Connector: exe.dev",
		},
		{
			name:           "md substitutes {{BASE_URL}} with request host",
			method:         http.MethodGet,
			path:           "/flue/exedev.md",
			wantHandled:    true,
			wantStatus:     http.StatusOK,
			wantCT:         "text/markdown; charset=utf-8",
			wantBodySubstr: "http://example.com/flue/exedev.ts",
		},
		{
			name:           "ts served as text/x-typescript",
			method:         http.MethodGet,
			path:           "/flue/exedev.ts",
			wantHandled:    true,
			wantStatus:     http.StatusOK,
			wantCT:         "text/x-typescript; charset=utf-8",
			wantBodySubstr: "export function exedev",
		},
		{
			name:        "unknown file under /flue/ returns false",
			method:      http.MethodGet,
			path:        "/flue/nope.txt",
			wantHandled: false,
		},
		{
			name:        "non-/flue path returns false",
			method:      http.MethodGet,
			path:        "/other/exedev.md",
			wantHandled: false,
		},
		{
			name:        "POST returns false",
			method:      http.MethodPost,
			path:        "/flue/exedev.md",
			wantHandled: false,
		},
		{
			name:        "bare /flue/ returns false",
			method:      http.MethodGet,
			path:        "/flue/",
			wantHandled: false,
		},
		{
			name:        "path traversal returns false",
			method:      http.MethodGet,
			path:        "/flue/../exedev.md",
			wantHandled: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			handled := Handle(w, req)
			if handled != tc.wantHandled {
				t.Fatalf("Handle returned %v, want %v", handled, tc.wantHandled)
			}
			if !tc.wantHandled {
				return
			}
			res := w.Result()
			if res.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if got := res.Header.Get("Content-Type"); got != tc.wantCT {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantCT)
			}
			if !strings.Contains(w.Body.String(), tc.wantBodySubstr) {
				t.Errorf("body missing substring %q", tc.wantBodySubstr)
			}
		})
	}
}
