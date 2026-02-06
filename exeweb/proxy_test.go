package exeweb

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"exe.dev/stage"
)

func TestNonProxyRedirect(t *testing.T) {
	t.Parallel()

	testEnv := stage.Test()

	testCases := []struct {
		name   string
		url    string
		expect string
	}{
		{
			name:   "exe.new redirects to /new",
			url:    "http://exe.new",
			expect: "http://" + testEnv.WebHost + "/new",
		},
		{
			name:   "exe.new with path still redirects to /new",
			url:    "http://exe.new/foo",
			expect: "http://" + testEnv.WebHost + "/new",
		},
		{
			name:   "exe.new with port redirects",
			url:    "http://exe.new:443",
			expect: "http://" + testEnv.WebHost + "/new",
		},
		{
			name:   "exe.new/moltbot redirects with prompt",
			url:    "http://exe.new/moltbot",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/moltbot"]),
		},
		{
			name:   "exe.new/clawdbot redirects with prompt",
			url:    "http://exe.new/clawdbot",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/clawdbot"]),
		},
		{
			name:   "exe.new/moltbot with invite passes through invite",
			url:    "http://exe.new/moltbot?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/moltbot"]) + "&invite=TESTCODE",
		},
		{
			name:   "exe.new/clawdbot with invite passes through invite",
			url:    "http://exe.new/clawdbot?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/clawdbot"]) + "&invite=TESTCODE",
		},
		{
			name:   "exe.new/openclaw redirects with prompt",
			url:    "http://exe.new/openclaw",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/openclaw"]),
		},
		{
			name:   "exe.new/openclaw with invite passes through invite",
			url:    "http://exe.new/openclaw?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?prompt=" + url.QueryEscape(ExeNewPathPrompts["/openclaw"]) + "&invite=TESTCODE",
		},
		{
			name:   "exe.new with invite but no prompt",
			url:    "http://exe.new/?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?invite=TESTCODE",
		},
		{
			name:   "WebHost does not redirect",
			url:    "http://" + testEnv.WebHost + "/new",
			expect: "",
		},
		{
			name:   "other domain does not redirect",
			url:    "http://other.test",
			expect: "",
		},
	}

	for _, tc := range testCases {
		r, err := http.NewRequest("GET", tc.url, bytes.NewReader(nil))
		if err != nil {
			t.Errorf("http.NewRequest(%q) failed: %v", tc.url, err)
			continue
		}
		target := NonProxyRedirect(&testEnv, r)
		if target != tc.expect {
			t.Errorf(`%s: NonProxyRedirect("test", %q) = %q, want %q`, tc.name, tc.url, target, tc.expect)
		}
	}
}

// TestIsProxyRequest tests the IsProxyRequest function
// with comprehensive cases.
func TestIsProxyRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		env      stage.Env
		host     string
		expected bool
		comment  string
	}{
		// Box:port format cases
		{
			name:     "invalid box:port (bad port)",
			env:      stage.Test(),
			host:     "mybox:abc",
			expected: false,
			comment:  "Should reject non-numeric ports",
		},
		{
			name:     "localhost:port should not be proxy",
			env:      stage.Test(),
			host:     "localhost:8080",
			expected: false,
			comment:  "localhost with port is the main domain, not a proxy request",
		},
		{
			name:     "exe.dev:port should not be proxy",
			env:      stage.Prod(),
			host:     "exe.dev:443",
			expected: false,
			comment:  "exe.dev with port is the main domain, not a proxy request",
		},

		// Subdomain format cases (dev mode)
		{
			name:     "dev subdomain format",
			env:      stage.Test(),
			host:     "mybox.exe.cloud",
			expected: true,
			comment:  "Should recognize *.exe.cloud pattern in dev mode",
		},
		{
			name:     "dev subdomain with server port",
			env:      stage.Test(),
			host:     "mybox.exe.cloud:8080",
			expected: true,
			comment:  "Should recognize *.exe.cloud even with server port",
		},
		{
			name:     "xterm subdomain",
			env:      stage.Test(),
			host:     "mybox.xterm.exe.cloud:8080",
			expected: false,
			comment:  "Should recognize xterm",
		},
		{
			name:     "shelley subdomain",
			env:      stage.Test(),
			host:     "mybox.shelley.exe.cloud:8080",
			expected: true,
			comment:  "Shelley subdomain is a proxy request (proxies to port 9999)",
		},
		{
			name:     "localhost alone in dev mode",
			env:      stage.Test(),
			host:     "localhost",
			expected: false,
			comment:  "Plain localhost should not be proxy request",
		},
		{
			name:     "deep subdomain in dev mode",
			env:      stage.Test(),
			host:     "box.team.exe.cloud",
			expected: true,
			comment:  "Should work with deeper subdomains",
		},

		// Subdomain format cases (production mode)
		{
			name:     "prod subdomain format",
			env:      stage.Prod(),
			host:     "mybox.exe.xyz",
			expected: true,
			comment:  "Should recognize *.exe.xyz (BoxHost) pattern in production",
		},
		{
			name:     "prod subdomain with server port",
			env:      stage.Prod(),
			host:     "mybox.exe.xyz:443",
			expected: true,
			comment:  "Should recognize *.exe.xyz even with server port",
		},
		{
			name:     "blog subdomain should proxy",
			env:      stage.Prod(),
			host:     "blog.exe.dev",
			expected: true,
			comment:  "blog.exe.dev is served from a VM even though it's under WebHost",
		},
		{
			name:     "prod WebHost subdomain should not be proxy",
			env:      stage.Prod(),
			host:     "mybox.exe.dev",
			expected: false,
			comment:  "Subdomains of WebHost (exe.dev) should not be proxy requests",
		},
		{
			name:     "exe.dev alone in prod mode",
			env:      stage.Prod(),
			host:     "exe.dev",
			expected: false,
			comment:  "Plain exe.dev should not be proxy request",
		},

		// Cross-mode cases: requests to "foreign" box domains go to proxy (which will 404)
		{
			name:     "prod BoxHost in dev mode",
			env:      stage.Test(),
			host:     "mybox.exe.xyz",
			expected: true,
			comment:  "Prod BoxHost subdomains are proxied in dev (not excluded)",
		},
		{
			name:     "dev BoxHost in prod mode",
			env:      stage.Prod(),
			host:     "mybox.exe.cloud",
			expected: true,
			comment:  "Dev BoxHost subdomains are proxied in prod (not excluded)",
		},

		// Edge cases
		{
			name:     "empty host",
			env:      stage.Test(),
			host:     "",
			expected: false,
			comment:  "Empty host should not be proxy request",
		},
		{
			name:     "just colon",
			env:      stage.Test(),
			host:     ":",
			expected: false,
			comment:  "Invalid format should be rejected",
		},
		{
			name:     "box with multiple colons",
			env:      stage.Test(),
			host:     "my:box:8080",
			expected: false,
			comment:  "Multiple colons should be rejected for box:port format",
		},
		{
			name:     "other domain",
			env:      stage.Test(),
			host:     "example.com",
			expected: true,
			comment:  "Other domains should be proxy requests",
		},
		{
			name:     "subdomain of other domain",
			env:      stage.Test(),
			host:     "mybox.example.com",
			expected: true,
			comment:  "Subdomains of other domains should be proxy requests",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := IsProxyRequest(&tc.env, "", tc.host)
			if result != tc.expected {
				t.Errorf("IsProxyRequest(%q, %q, %q) = %t, want %t\nComment %s", tc.env.String(), "", tc.host, result, tc.expected, tc.comment)
			} else {
				t.Logf("✓ %s: host=%q stage=%s -> %v", tc.comment, tc.host, tc.env.String(), result)
			}
		})
	}
}

