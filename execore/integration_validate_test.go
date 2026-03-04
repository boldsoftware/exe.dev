package execore

import "testing"

func TestValidateIntegrationName(t *testing.T) {
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
		string(make([]byte, 64)), // 64 chars, too long
	}
	for _, name := range bad {
		if err := validateIntegrationName(name); err == nil {
			t.Errorf("validateIntegrationName(%q) = nil, want error", name)
		}
	}
}

func TestValidateHTTPHeader(t *testing.T) {
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
	}
	for _, h := range bad {
		if err := validateHTTPHeader(h); err == nil {
			t.Errorf("validateHTTPHeader(%q) = nil, want error", h)
		}
	}
}

func TestValidateTargetURL(t *testing.T) {
	good := []string{
		"https://example.com/api",
		"https://api.example.com/v1/",
		"https://httpbin.org/anything",
		"https://user:pass@example.com/api", // credentials are allowed
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
		"https://example.com:8080/api",   // non-443 port
		"https://localhost/api",          // localhost
		"https://foo.localhost/api",      // subdomain of localhost
		"https://printer.local",          // mDNS .local
		"https://corp.internal",          // .internal
		"https://myhost.ts.net",          // Tailscale
		"https://myhost.tail1234.ts.net", // Tailscale subdomain
		"https://test.exe.cloud",         // exe.cloud
		"https://test.exe.dev",           // exe.dev
	}
	for _, u := range bad {
		if err := validateTargetURL(u); err == nil {
			t.Errorf("validateTargetURL(%q) = nil, want error", u)
		}
	}
}
