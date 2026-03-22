package exeprox

import (
	"context"
	"errors"
	"testing"

	"exe.dev/exeweb"
)

// mockExeproxData implements just enough of ExeproxData for ValidateAppToken tests.
// CookieInfo is the only method called; all others panic if invoked.
type mockExeproxData struct {
	ExeproxData
	cookieInfo func(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error)
}

func (m *mockExeproxData) CookieInfo(ctx context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	return m.cookieInfo(ctx, cookieValue, domain)
}

func newTestExewebProxyData(ed ExeproxData) *exewebProxyData {
	p := &Proxy{exeproxData: ed}
	p.web.proxy = p
	return &exewebProxyData{wp: &p.web}
}

func TestValidateAppToken(t *testing.T) {
	t.Parallel()

	const testUserID = "user-42"
	validToken := exeweb.AppTokenPrefix + "secret123"

	t.Run("valid_app_token", func(t *testing.T) {
		epd := newTestExewebProxyData(&mockExeproxData{
			cookieInfo: func(_ context.Context, cookieValue, domain string) (exeweb.CookieData, bool, error) {
				if cookieValue != validToken {
					return exeweb.CookieData{}, false, nil
				}
				return exeweb.CookieData{UserID: testUserID}, true, nil
			},
		})
		uid, err := epd.ValidateAppToken(context.Background(), validToken)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if uid != testUserID {
			t.Fatalf("got userID %q, want %q", uid, testUserID)
		}
	})

	t.Run("missing_prefix_rejected", func(t *testing.T) {
		called := false
		epd := newTestExewebProxyData(&mockExeproxData{
			cookieInfo: func(context.Context, string, string) (exeweb.CookieData, bool, error) {
				called = true
				return exeweb.CookieData{}, false, nil
			},
		})
		_, err := epd.ValidateAppToken(context.Background(), "not-an-app-token")
		if err == nil {
			t.Fatal("expected error for token without prefix")
		}
		if called {
			t.Fatal("CookieInfo should not be called for tokens without the app prefix")
		}
	})

	t.Run("cookie_not_found", func(t *testing.T) {
		epd := newTestExewebProxyData(&mockExeproxData{
			cookieInfo: func(context.Context, string, string) (exeweb.CookieData, bool, error) {
				return exeweb.CookieData{}, false, nil
			},
		})
		_, err := epd.ValidateAppToken(context.Background(), validToken)
		if err == nil {
			t.Fatal("expected error for unknown token")
		}
	})

	t.Run("cookie_info_error_propagated", func(t *testing.T) {
		wantErr := errors.New("rpc failed")
		epd := newTestExewebProxyData(&mockExeproxData{
			cookieInfo: func(context.Context, string, string) (exeweb.CookieData, bool, error) {
				return exeweb.CookieData{}, false, wantErr
			},
		})
		_, err := epd.ValidateAppToken(context.Background(), validToken)
		if !errors.Is(err, wantErr) {
			t.Fatalf("got error %v, want %v", err, wantErr)
		}
	})
}
