package execore

import "testing"

func TestValidateIntegrationName(t *testing.T) {
	t.Parallel()
	good := []string{
		"a", "myproxy", "my-proxy", "a1", "test-123", "x",
	}
	for _, name := range good {
		if err := validateIntegrationName(name); err != nil {
			t.Errorf("validateIntegrationName(%q) = %v, want nil", name, err)
		}
	}

	bad := []string{
		"", "-start", "end-", "UPPER", "has space", "has.dot",
		"has_underscore", "a/b", "a:b",
		"notify",                 // reserved for built-in integration
		string(make([]byte, 64)), // 64 chars, too long
		"exe-foo",                // reserved prefix
		"exe-",                   // reserved prefix (bare)
	}
	for _, name := range bad {
		if err := validateIntegrationName(name); err == nil {
			t.Errorf("validateIntegrationName(%q) = nil, want error", name)
		}
	}
}

func TestValidateHTTPHeader(t *testing.T) {
	t.Parallel()
	good := []string{
		"Authorization:Bearer tok",
		"X-Custom-Auth:secret",
		"X-Api-Key: my-key",
	}
	for _, h := range good {
		if err := validateHTTPHeader(h); err != nil {
			t.Errorf("validateHTTPHeader(%q) = %v, want nil", h, err)
		}
	}

	bad := []string{
		"novalue",                 // no colon
		":emptykey",               // empty key
		"has space:value",         // space in key
		"X-Exedev-Box:x",          // reserved
		"host:evil",               // reserved
		"X-Foo:",                  // empty value
		"X-Foo:val\r\nX-Evil:inj", // CRLF injection

		// Hop-by-hop / connection-management headers.
		"Transfer-Encoding:chunked",   // request smuggling
		"Content-Length:0",            // request smuggling
		"Connection:keep-alive",       // hop-by-hop
		"Connection:upgrade",          // hop-by-hop
		"Upgrade:websocket",           // hop-by-hop
		"Te:trailers",                 // hop-by-hop
		"Trailer:X-Foo",               // hop-by-hop
		"Proxy-Authorization:Basic x", // proxy header
		"Proxy-Connection:keep-alive", // proxy header
		"transfer-encoding:chunked",   // lowercase variant
	}
	for _, h := range bad {
		if err := validateHTTPHeader(h); err == nil {
			t.Errorf("validateHTTPHeader(%q) = nil, want error", h)
		}
	}
}

func TestValidateTargetURL(t *testing.T) {
	t.Parallel()
	good := []string{
		"https://example.com",
		"https://api.example.com/",
		"https://httpbin.org",
		"https://user:pass@example.com",     // credentials are allowed
		"https://example.com:8080",          // non-standard port
		"https://example.com:443",           // explicit standard port
		"https://registry.example.com:5000", // registry-style port
	}
	for _, u := range good {
		if err := validateTargetURL(u); err != nil {
			t.Errorf("validateTargetURL(%q) = %v, want nil", u, err)
		}
	}

	bad := []string{
		"ftp://example.com",              // bad scheme
		"http://example.com",             // not https
		"example.com",                    // no scheme
		"https://",                       // no host
		"https://192.168.1.1/api",        // bare IP
		"https://10.0.0.1:8080/api",      // bare IP with port
		"https://example.com:8080/api",   // path not allowed (port is fine)
		"https://localhost/api",          // localhost
		"https://foo.localhost/api",      // subdomain of localhost
		"https://printer.local",          // mDNS .local
		"https://corp.internal",          // .internal
		"https://myhost.ts.net",          // Tailscale
		"https://myhost.tail1234.ts.net", // Tailscale subdomain
		"https://test.exe.cloud",         // exe.cloud
		"https://test.exe.dev",           // exe.dev
		"https://example.com/api",        // path not allowed
		"https://api.example.com/v1/",    // path not allowed
		"https://httpbin.org/anything",   // path not allowed
		"https://evil.exe.cloud.",        // trailing dot bypass
		"https://evil.exe.dev.",          // trailing dot bypass
		"https://evil.ts.net.",           // trailing dot bypass
		"https://localhost.",             // trailing dot bypass
		"https://evil.internal.",         // trailing dot bypass
		"https://evil.local.",            // trailing dot bypass
	}
	for _, u := range bad {
		if err := validateTargetURL(u); err == nil {
			t.Errorf("validateTargetURL(%q) = nil, want error", u)
		}
	}
}
