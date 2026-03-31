package templates

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// proxyTemplates lists templates rendered on VM subdomains (box.exe.xyz).
// These must be fully self-contained: no external CSS or JS.
//
// Why: VM subdomains proxy requests to the container. When the proxy renders
// an error page (503, unreachable, access denied), it cannot serve /static/
// assets — those requests would either require authentication or get forwarded
// to the container. So proxy-rendered templates must inline all their styles
// and not reference any external resources.
var proxyTemplates = []string{
	"proxy-401.html",
	"proxy-503.html",
	"proxy-unreachable.html",
	"proxy-request-access.html",
	"proxy-request-sent.html",
	"proxy-terminal-access-denied.html",
}

var (
	linkStylesheetRe = regexp.MustCompile(`<link[^>]+rel=["']stylesheet["'][^>]*>`)
	scriptSrcRe      = regexp.MustCompile(`<script[^>]+src=["'][^"']+["'][^>]*>`)
)

func TestProxyTemplatesAreSelfContained(t *testing.T) {
	for _, name := range proxyTemplates {
		t.Run(name, func(t *testing.T) {
			data, err := fs.ReadFile(Files, name)
			if err != nil {
				t.Fatalf("proxy template %s not found in embedded files: %v", name, err)
			}
			content := string(data)

			if matches := linkStylesheetRe.FindAllString(content, -1); len(matches) > 0 {
				for _, m := range matches {
					t.Errorf("external stylesheet not allowed in proxy template:\n  %s", m)
				}
			}

			if matches := scriptSrcRe.FindAllString(content, -1); len(matches) > 0 {
				for _, m := range matches {
					t.Errorf("external script not allowed in proxy template:\n  %s", m)
				}
			}

			if !strings.Contains(content, "<style>") {
				t.Error("proxy template should have an inline <style> block")
			}
		})
	}
}
