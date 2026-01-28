// Package testinfra provides Playwright browser automation support for e1e tests.
package testinfra

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/playwright-community/playwright-go"
)

var (
	playwrightOnce    sync.Once
	playwrightErr     error
	playwrightInst    *playwright.Playwright
	playwrightBrowser playwright.Browser
)

// StartPlaywright initializes the Playwright runtime and browser.
// It should be called once in TestMain. It is safe to call multiple times;
// subsequent calls are no-ops.
func StartPlaywright() error {
	playwrightOnce.Do(func() {
		// Install browsers if needed (this is a no-op if already installed)
		installErr := playwright.Install()
		if installErr != nil {
			playwrightErr = fmt.Errorf("failed to install playwright browsers: %w", installErr)
			return
		}

		pw, err := playwright.Run()
		if err != nil {
			playwrightErr = fmt.Errorf("failed to start playwright: %w", err)
			return
		}
		playwrightInst = pw

		browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(true),
		})
		if err != nil {
			pw.Stop()
			playwrightErr = fmt.Errorf("failed to launch chromium: %w", err)
			return
		}
		playwrightBrowser = browser
	})
	return playwrightErr
}

// StopPlaywright stops the browser and Playwright runtime.
// Safe to call even if StartPlaywright was never called.
func StopPlaywright() {
	if playwrightBrowser != nil {
		playwrightBrowser.Close()
	}
	if playwrightInst != nil {
		playwrightInst.Stop()
	}
}

// PlaywrightAvailable returns true if Playwright has been successfully initialized.
func PlaywrightAvailable() bool {
	return playwrightErr == nil && playwrightBrowser != nil
}

// NewPage creates a new browser page (tab) for testing.
// The caller is responsible for calling page.Close() when done.
func NewPage() (playwright.Page, error) {
	if !PlaywrightAvailable() {
		return nil, fmt.Errorf("playwright not available: %v", playwrightErr)
	}
	return playwrightBrowser.NewPage()
}

// NewPageWithCookies creates a new browser page with pre-set cookies.
// This is useful when you need to authenticate as a user who was registered via SSH.
func NewPageWithCookies(baseURL string, cookies []playwright.OptionalCookie) (playwright.Page, error) {
	if !PlaywrightAvailable() {
		return nil, fmt.Errorf("playwright not available: %v", playwrightErr)
	}

	context, err := playwrightBrowser.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to create browser context: %w", err)
	}

	if len(cookies) > 0 {
		err = context.AddCookies(cookies)
		if err != nil {
			context.Close()
			return nil, fmt.Errorf("failed to add cookies: %w", err)
		}
	}

	page, err := context.NewPage()
	if err != nil {
		context.Close()
		return nil, fmt.Errorf("failed to create page: %w", err)
	}

	return page, nil
}

// HTTPCookiesToPlaywright converts net/http cookies to Playwright cookies.
// The baseURL is used to set the domain and URL for the cookies.
func HTTPCookiesToPlaywright(baseURL string, httpCookies []*http.Cookie) []playwright.OptionalCookie {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	domain := u.Hostname()

	cookies := make([]playwright.OptionalCookie, 0, len(httpCookies))
	for _, c := range httpCookies {
		pwCookie := playwright.OptionalCookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: &domain,
			Path:   playwright.String("/"),
		}
		if c.Secure {
			pwCookie.Secure = playwright.Bool(true)
		}
		if c.HttpOnly {
			pwCookie.HttpOnly = playwright.Bool(true)
		}
		cookies = append(cookies, pwCookie)
	}
	return cookies
}
