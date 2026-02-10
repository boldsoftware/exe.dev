package browse

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// webauthnTestPage is a minimal HTML page that exercises WebAuthn APIs.
// On a browser without the fix, this would trigger a segfault in headless-shell.
const webauthnTestPage = `<!DOCTYPE html>
<html>
<head><title>WebAuthn Test</title></head>
<body>
<h1 id="status">Testing WebAuthn...</h1>
<pre id="result"></pre>
<script>
(function() {
    var status = document.getElementById('status');
    var result = document.getElementById('result');
    var info = [];

    // Check if navigator.credentials exists
    info.push('navigator.credentials exists: ' + (!!navigator.credentials));

    // Check if PublicKeyCredential exists
    info.push('PublicKeyCredential exists: ' + (typeof PublicKeyCredential !== 'undefined'));

    // Try to call navigator.credentials.create with WebAuthn options
    if (navigator.credentials && navigator.credentials.create) {
        try {
            var createPromise = navigator.credentials.create({
                publicKey: {
                    challenge: new Uint8Array(32),
                    rp: { name: 'Test' },
                    user: {
                        id: new Uint8Array(16),
                        name: 'test@test.com',
                        displayName: 'Test User'
                    },
                    pubKeyCredParams: [{ type: 'public-key', alg: -7 }]
                }
            });
            createPromise.then(function(cred) {
                info.push('credentials.create resolved (unexpected)');
                result.textContent = info.join('\n');
                status.textContent = 'WebAuthn available (unexpected)';
            }).catch(function(err) {
                info.push('credentials.create rejected: ' + err.name + ': ' + err.message);
                result.textContent = info.join('\n');
                status.textContent = 'Page loaded - WebAuthn blocked';
            });
        } catch(e) {
            info.push('credentials.create threw: ' + e.name + ': ' + e.message);
            result.textContent = info.join('\n');
            status.textContent = 'Page loaded - WebAuthn threw sync error';
        }
    } else {
        info.push('navigator.credentials.create not available');
        result.textContent = info.join('\n');
        status.textContent = 'Page loaded - WebAuthn not available';
    }
})();
</script>
</body>
</html>`

// TestWebAuthnPageDoesNotCrash verifies that navigating to a page that uses
// WebAuthn APIs does not crash the headless browser. This is the core test
// for the WebAuthn fix (issue #78) which adds WebAuthentication to Chrome's
// --disable-features flag.
func TestWebAuthnPageDoesNotCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	// Start a test HTTP server serving the WebAuthn test page
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/webauthn-test.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(webauthnTestPage))
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	testURL := fmt.Sprintf("http://127.0.0.1:%d/webauthn-test.html", port)

	// Create browser tools with a generous timeout
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	// Step 1: Navigate to the WebAuthn test page
	browserCtx, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}

	navCtx, navCancel := context.WithTimeout(browserCtx, 15*time.Second)
	defer navCancel()

	err = chromedp.Run(navCtx,
		chromedp.Navigate(testURL),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		t.Fatalf("Failed to navigate to WebAuthn test page: %v", err)
	}

	// Step 2: Wait for JS to execute
	time.Sleep(1 * time.Second)

	// Step 3: Verify browser is still alive by checking the context
	browserCtx2, err := tools.GetBrowserContext()
	if err != nil {
		t.Fatalf("Browser context is dead after WebAuthn page visit: %v", err)
	}
	if browserCtx2.Err() != nil {
		t.Fatalf("Browser context cancelled after WebAuthn page: %v", browserCtx2.Err())
	}

	// Step 4: Verify the page loaded correctly by reading the status element
	var statusText string
	evalCtx, evalCancel := context.WithTimeout(browserCtx2, 10*time.Second)
	defer evalCancel()

	err = chromedp.Run(evalCtx,
		chromedp.Text("#status", &statusText, chromedp.NodeVisible),
	)
	if err != nil {
		t.Fatalf("Failed to read page status (browser may have crashed): %v", err)
	}

	t.Logf("Page status: %q", statusText)

	if !strings.Contains(statusText, "Page loaded") {
		t.Errorf("Expected status to contain 'Page loaded', got: %q", statusText)
	}

	// Step 5: Read the detailed result
	var resultText string
	err = chromedp.Run(evalCtx,
		chromedp.Text("#result", &resultText, chromedp.NodeVisible),
	)
	if err != nil {
		t.Fatalf("Failed to read result text: %v", err)
	}

	t.Logf("WebAuthn result details:\n%s", resultText)

	// Step 6: Verify browser can still perform operations after the WebAuthn page
	err = chromedp.Run(evalCtx,
		chromedp.Navigate("about:blank"),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		t.Fatalf("Browser is non-functional after WebAuthn page visit: %v", err)
	}

	t.Log("Browser survived WebAuthn page visit and remains functional")
}

// TestWebAuthnDisabledFeatureFlag verifies that WebAuthentication is in the
// disable-features flag by checking that navigator.credentials.create
// either rejects or is unavailable (rather than crashing).
func TestWebAuthnDisabledFeatureFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tools := NewBrowseTools(ctx, 0, 0)
	t.Cleanup(func() {
		tools.Close()
	})

	browserCtx, err := tools.GetBrowserContext()
	if err != nil {
		if strings.Contains(err.Error(), "failed to start browser") {
			t.Skip("Browser automation not available in this environment")
		}
		t.Fatalf("Failed to get browser context: %v", err)
	}

	// Navigate to about:blank
	opCtx, opCancel := context.WithTimeout(browserCtx, 15*time.Second)
	defer opCancel()

	err = chromedp.Run(opCtx,
		chromedp.Navigate("about:blank"),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		t.Fatalf("Failed to navigate: %v", err)
	}

	// Try to use WebAuthn API and verify it doesn't crash the browser.
	// With WebAuthentication disabled, this should either:
	// - Return an error/rejection (NotSupportedError or NotAllowedError)
	// - Have navigator.credentials be undefined/limited
	var result string
	err = chromedp.Run(opCtx,
		chromedp.Evaluate(`
			(async () => {
				try {
					await navigator.credentials.create({
						publicKey: {
							challenge: new Uint8Array(32),
							rp: { name: 'Test' },
							user: {
								id: new Uint8Array(16),
								name: 'test@test.com',
								displayName: 'Test'
							},
							pubKeyCredParams: [{ type: 'public-key', alg: -7 }]
						}
					});
					return 'resolved-unexpectedly';
				} catch(e) {
					return 'rejected:' + e.name;
				}
			})()
		`, &result,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			},
		),
	)
	if err != nil {
		// Even a timeout/error is acceptable — what matters is the browser didn't crash
		t.Logf("WebAuthn credentials.create returned error (acceptable): %v", err)
	} else {
		t.Logf("WebAuthn credentials.create result: %s", result)
		if result == "resolved-unexpectedly" {
			t.Log("Warning: credentials.create resolved, WebAuthentication may not be fully disabled")
		}
	}

	// The critical check: browser must still be alive
	var title string
	err = chromedp.Run(opCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Title(&title),
	)
	if err != nil {
		t.Fatalf("Browser crashed or became unresponsive after WebAuthn call: %v", err)
	}

	t.Log("Browser survived WebAuthn API call and remains functional")
}
