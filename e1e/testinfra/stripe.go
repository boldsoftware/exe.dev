package testinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"exe.dev/billing/httprr"
)

// HasStripeTestKey reports whether a Stripe test key is configured.
// E1e tests that require real Stripe integration should skip when this returns false.
func HasStripeTestKey() bool {
	return os.Getenv("STRIPE_SECRET_KEY") != ""
}

// SkipWithoutStripe skips the test if no Stripe test key is available.
func SkipWithoutStripe(t interface{ Skip(...any) }) {
	if !HasStripeTestKey() {
		t.Skip("STRIPE_SECRET_KEY not set, skipping Stripe integration test")
	}
}

// StripeProxy is an HTTP proxy that records/replays Stripe API traffic
// using httprr. Start it with [StartStripeProxy].
//
// In record mode (-httprecord flag), requests are forwarded to real Stripe
// and responses are saved to a cassette file. In replay mode, responses
// come from the cassette.
//
// Checkout session completion is handled synthetically via
// [StripeProxy.MarkSessionCompleted] because Stripe has no API to
// complete a checkout session — that can only happen in a browser.
type StripeProxy struct {
	Port int
	ln   net.Listener
	rr   *httprr.RecordReplay

	mu                sync.Mutex
	completedSessions map[string]completedSession
}

type completedSession struct {
	SessionID  string
	CustomerID string
}

// StartStripeProxy starts an HTTP server on localhost that proxies requests
// to api.stripe.com through an httprr recorder/replayer.
//
// Request scrubbing normalizes non-deterministic values (billing IDs, URLs,
// timestamps) so that the cassette replays correctly regardless of test run.
//
// The caller must call Close when done.
func StartStripeProxy(cassettePath string) (*StripeProxy, error) {
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		return nil, fmt.Errorf("mkdir testdata: %w", err)
	}

	rr, err := httprr.Open(cassettePath, http.DefaultTransport)
	if err != nil {
		return nil, fmt.Errorf("httprr.Open(%q): %w", cassettePath, err)
	}

	rr.ScrubReq(func(r *http.Request) error {
		// Normalize exe_ billing IDs in URL path.
		pathParts := strings.Split(r.URL.Path, "/")
		for i, part := range pathParts {
			if strings.HasPrefix(part, "exe_") {
				pathParts[i] = "exe_TEST"
			}
		}
		r.URL.Path = strings.Join(pathParts, "/")

		// Normalize query parameters.
		q := r.URL.Query()
		for _, key := range []string{"created[gt]", "created[gte]"} {
			if q.Has(key) {
				q.Set(key, "1")
			}
		}
		if customer := q.Get("customer"); strings.HasPrefix(customer, "exe_") {
			q.Set("customer", "exe_TEST")
		}
		// Strip expand params so recordings match with or without expansions.
		for key := range q {
			if strings.HasPrefix(key, "expand") {
				q.Del(key)
			}
		}
		r.URL.RawQuery = q.Encode()

		// Strip non-deterministic headers.
		r.Header.Del("Authorization")
		r.Header.Del("Idempotency-Key")
		r.Header.Del("Stripe-Version")
		r.Header.Del("User-Agent")
		r.Header.Del("X-Stripe-Client-User-Agent")
		r.Header.Del("X-Stripe-Client-Telemetry")

		// Normalize form-encoded body parameters.
		if body, ok := r.Body.(*httprr.Body); ok {
			contentType := r.Header.Get("Content-Type")
			if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
				form, parseErr := url.ParseQuery(string(body.Data))
				if parseErr == nil {
					if id := form.Get("id"); strings.HasPrefix(id, "exe_") {
						form.Set("id", "exe_TEST")
					}
					if customer := form.Get("customer"); strings.HasPrefix(customer, "exe_") {
						form.Set("customer", "exe_TEST")
					}
					for _, key := range []string{"success_url", "cancel_url", "return_url"} {
						if form.Has(key) {
							form.Set(key, "https://example.com/callback")
						}
					}
					if email := form.Get("email"); email != "" {
						form.Set("email", "test@example.com")
					}
					body.Data = []byte(form.Encode())
					body.ReadOffset = 0
				}
			}
		}
		return nil
	})

	p := &StripeProxy{
		rr:                rr,
		completedSessions: make(map[string]completedSession),
	}

	stripeURL, _ := url.Parse("https://api.stripe.com")
	proxy := &httputil.ReverseProxy{
		Transport: rr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(stripeURL)
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = stripeURL.Host
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intercept completed checkout session retrieval.
		// Stripe has no API to complete a checkout session, so we
		// synthesize a "complete" response for sessions marked by the test.
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			sessID := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
			p.mu.Lock()
			cs, ok := p.completedSessions[sessID]
			p.mu.Unlock()
			if ok {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"id":             cs.SessionID,
					"object":         "checkout.session",
					"status":         "complete",
					"payment_status": "paid",
					"customer":       map[string]any{"id": cs.CustomerID},
				})
				return
			}
		}

		proxy.ServeHTTP(w, r)
	})

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		rr.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}

	srv := &http.Server{Handler: handler}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("stripe proxy: %v", err)
		}
	}()

	p.Port = ln.Addr().(*net.TCPAddr).Port
	p.ln = ln
	return p, nil
}

// URL returns the base URL of the proxy (e.g. "http://localhost:12345").
func (p *StripeProxy) URL() string {
	return fmt.Sprintf("http://localhost:%d", p.Port)
}

// Close stops the proxy and flushes any httprr recordings.
func (p *StripeProxy) Close() error {
	p.ln.Close()
	return p.rr.Close()
}

// MarkSessionCompleted registers a checkout session as completed so that
// subsequent GET /v1/checkout/sessions/<id> requests return a synthetic
// "complete" response. This is needed because Stripe checkout sessions
// can only be completed in a browser — there is no API to complete them.
func (p *StripeProxy) MarkSessionCompleted(sessionID, customerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completedSessions[sessionID] = completedSession{
		SessionID:  sessionID,
		CustomerID: customerID,
	}
}

// CompleteStripeCheckout programmatically completes a Stripe checkout flow.
// It:
//  1. Hits /billing/update?source=e1e to create a checkout session
//  2. Extracts the checkout session ID from the redirect URL
//  3. Marks the session as completed in the proxy
//  4. Hits /billing/success?session_id=<id> to activate the account
//
// cookies should be the auth cookies for the authenticated user.
func (se *ServerEnv) CompleteStripeCheckout(ctx context.Context, cookies []*http.Cookie, stripeProxy *StripeProxy) error {
	// Step 1: Hit /billing/update to create the checkout session.
	billingURL := fmt.Sprintf("http://localhost:%d/billing/update?source=e1e", se.Exed.HTTPPort)
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", billingURL, nil)
	if err != nil {
		return fmt.Errorf("create billing request: %w", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("billing update request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("expected redirect from /billing/update, got %d: %s", resp.StatusCode, body)
	}

	// Step 2: Extract checkout session ID from the redirect URL.
	checkoutURL := resp.Header.Get("Location")
	if checkoutURL == "" {
		return fmt.Errorf("no Location header in /billing/update redirect")
	}

	u, err := url.Parse(checkoutURL)
	if err != nil {
		return fmt.Errorf("parse checkout URL %q: %w", checkoutURL, err)
	}
	sessionID := path.Base(u.Path)
	if !strings.HasPrefix(sessionID, "cs_") {
		return fmt.Errorf("unexpected checkout session ID %q from URL %q", sessionID, checkoutURL)
	}

	// Step 3: Mark the session as completed in the proxy.
	// VerifyCheckout checks that customer is non-empty and returns it,
	// but handleBillingSuccess uses the canonical account from the DB,
	// not the returned customer ID.
	stripeProxy.MarkSessionCompleted(sessionID, "cus_e1e_test")

	// Step 4: Hit /billing/success to activate the account.
	successURL := fmt.Sprintf("http://localhost:%d/billing/success?session_id=%s",
		se.Exed.HTTPPort, url.QueryEscape(sessionID))
	req, err = http.NewRequestWithContext(ctx, "GET", successURL, nil)
	if err != nil {
		return fmt.Errorf("create success request: %w", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp2, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("billing success request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp2.Body); resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK && resp2.StatusCode != http.StatusSeeOther && resp2.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("/billing/success returned %d: %s", resp2.StatusCode, body)
	}

	return nil
}

// QueryUserPlanByEmail returns the plan_id for the given email address by
// querying /debug/users?format=json. Returns empty string if the user has no
// active plan.
func (se *ServerEnv) QueryUserPlanByEmail(email string) (string, error) {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/users?format=json", se.Exed.HTTPPort))
	if err != nil {
		return "", fmt.Errorf("GET /debug/users: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/debug/users returned %d", resp.StatusCode)
	}

	var users []struct {
		Email  string `json:"email"`
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", fmt.Errorf("decode /debug/users: %w", err)
	}
	for _, u := range users {
		if u.Email == email {
			return u.PlanID, nil
		}
	}
	return "", fmt.Errorf("user %q not found", email)
}
