package exeprox

import (
	"context"
	"time"

	"exe.dev/exeweb"

	"github.com/go4org/hashtriemap"
)

// cookiesData holds information about authentication cookies.
type cookiesData struct {
	cookies hashtriemap.HashTrieMap[string, exeweb.CookieData]
}

// lookup returns the information about a cookie given its value and domain.
// The bool result reports whether the cookie exists.
func (cd *cookiesData) lookup(ctx context.Context, exeproxData ExeproxData, cookieValue, domain string) (exeweb.CookieData, bool, error) {
	data, ok := cd.cookies.Load(cookieValue)
	if ok {
		if data.Domain == domain && time.Now().Before(data.ExpiresAt) {
			return data, true, nil
		}
		if data.Domain != domain {
			return exeweb.CookieData{}, false, nil
		}
		// Expired — evict and re-fetch below.
		cd.cookies.Delete(cookieValue)
	}

	data, exists, err := exeproxData.CookieInfo(ctx, cookieValue, domain)

	if err == nil && exists {
		cd.cookies.Store(cookieValue, data)
	}

	return data, exists, err
}

// clear clears the boxes cache.
func (cd *cookiesData) clear() {
	cd.cookies.Clear()
}

// deleteCookie deletes information about a cookie.
// This is called when we receive a notification from exed
// about a deleted cookie.
func (cd *cookiesData) deleteCookie(ctx context.Context, cookieValue string) {
	cd.cookies.Delete(cookieValue)
}

// deleteCookiesForUser deletes all cookies for a user.
func (cd *cookiesData) deleteCookiesForUser(ctx context.Context, userID string) {
	for cookieValue, cookieData := range cd.cookies.All() {
		if cookieData.UserID == userID {
			cd.cookies.Delete(cookieValue)
		}
	}
}
