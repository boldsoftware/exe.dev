package exeprox

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testListener struct {
	addr net.Addr
}

func (l testListener) Accept() (net.Conn, error) { return nil, nil }
func (l testListener) Close() error              { return nil }
func (l testListener) Addr() net.Addr            { return l.addr }

func TestHTTPToHTTPSHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		host         string
		path         string
		httpsPort    int
		wantLocation string
	}{
		{
			name:         "standard port",
			host:         "example.com:80",
			path:         "/foo",
			httpsPort:    443,
			wantLocation: "https://example.com/foo",
		},
		{
			name:         "host without port",
			host:         "example.com",
			path:         "/",
			httpsPort:    443,
			wantLocation: "https://example.com/",
		},
		{
			name:         "non-standard HTTPS port",
			host:         "example.com:80",
			path:         "/bar",
			httpsPort:    8443,
			wantLocation: "https://example.com:8443/bar",
		},
		{
			name:         "IPv6 standard port",
			host:         "[::1]:80",
			path:         "/",
			httpsPort:    443,
			wantLocation: "https://::1/",
		},
		{
			name:         "IPv6 non-standard port",
			host:         "[::1]:80",
			path:         "/test",
			httpsPort:    8443,
			wantLocation: "https://[::1]:8443/test",
		},
		{
			name:         "preserves query string",
			host:         "example.com:80",
			path:         "/path?key=value",
			httpsPort:    443,
			wantLocation: "https://example.com/path?key=value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wp := &WebProxy{
				httpsLn: &listener{
					ln: testListener{addr: &net.TCPAddr{Port: tt.httpsPort}},
				},
			}
			handler := wp.httpToHTTPSHandler()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusMovedPermanently {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusMovedPermanently)
			}
			if loc := rr.Header().Get("Location"); loc != tt.wantLocation {
				t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
			}
		})
	}
}
