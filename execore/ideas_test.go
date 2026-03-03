package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/idea"
)

func TestNewPagePrefillsFromIdeaShortname(t *testing.T) {
	// Test that /new/<shortname> and /new?idea=<shortname> prefill from the DB.
	server := newTestServer(t)

	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	for _, path := range []string{"/new/openclaw", "/new?idea=openclaw"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("%s: expected status 200, got %d", path, w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, `value="openclaw-`) {
			t.Errorf("%s: expected hostname prefilled with 'openclaw-<suffix>', got body without it", path)
		}
		if !strings.Contains(body, "Openclaw") {
			t.Errorf("%s: expected prompt to contain 'Openclaw'", path)
		}
	}
}

func TestNewPagePrefillsImageFromIdeaTemplate(t *testing.T) {
	// Test that an image-only idea template prefills the image field, not the prompt.
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	for _, path := range []string{"/new/marimo", "/new?idea=marimo"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = server.env.WebHost
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("%s: expected status 200, got %d", path, w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, `value="marimo-`) {
			t.Errorf("%s: expected hostname prefilled with 'marimo-<suffix>'", path)
		}
		// Image field should be prefilled.
		if !strings.Contains(body, `value="marimo-team/marimo:latest-sql"`) {
			t.Errorf("%s: expected image field prefilled with marimo image", path)
		}
		// Prompt textarea should be empty (image-only template has no prompt).
		if strings.Contains(body, "ghcr.io/marimo") {
			t.Errorf("%s: expected no ghcr.io reference in body (old prompt text)", path)
		}
	}
}

func TestSeedDefaultTemplatesUpdatesExistingTemplatePrompt(t *testing.T) {
	server := newTestServer(t)

	slug := "zulip-chat"
	stalePrompt := "outdated prompt"

	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	existing, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, slug)
	if err != nil {
		t.Fatalf("failed to fetch seeded template: %v", err)
	}

	err = withTx1(server, t.Context(), (*exedb.Queries).UpdateTemplate, exedb.UpdateTemplateParams{
		ID:               existing.ID,
		Title:            existing.Title,
		ShortDescription: existing.ShortDescription,
		Category:         existing.Category,
		Prompt:           stalePrompt,
		IconURL:          existing.IconURL,
		ScreenshotURL:    existing.ScreenshotURL,
		Featured:         existing.Featured,
		VMShortname:      existing.VMShortname,
		Image:            existing.Image,
	})
	if err != nil {
		t.Fatalf("failed to set stale prompt: %v", err)
	}

	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	updated, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, slug)
	if err != nil {
		t.Fatalf("failed to fetch template after reseed: %v", err)
	}

	var expectedPrompt string
	for _, tmpl := range idea.SeedTemplates {
		if tmpl.Slug == slug {
			expectedPrompt = tmpl.Prompt
			break
		}
	}
	if expectedPrompt == "" {
		t.Fatalf("seed template %q not found", slug)
	}

	if updated.Prompt != expectedPrompt {
		t.Fatalf("expected prompt to be reset by reseed")
	}
	if updated.Prompt == stalePrompt {
		t.Fatalf("expected prompt to change from stale value")
	}
}

func TestIdeasRedirect(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ideas", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("/ideas: expected 301, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/idea" {
		t.Fatalf("/ideas: expected redirect to /idea, got %q", loc)
	}
}

func TestIdeaSlugPath(t *testing.T) {
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/idea/openclaw", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/idea/openclaw: expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "idea-page") {
		t.Fatalf("/idea/openclaw: expected idea page content")
	}
}

func TestIdeaDeployCountIncrement(t *testing.T) {
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	// Check initial deploy count is 0
	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}
	if tmpl.DeployCount != 0 {
		t.Fatalf("expected initial deploy_count=0, got %d", tmpl.DeployCount)
	}

	// Increment
	if err := withTx1(server, t.Context(), (*exedb.Queries).IncrementTemplateDeployCount, "openclaw"); err != nil {
		t.Fatalf("IncrementTemplateDeployCount failed: %v", err)
	}

	tmpl, err = withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}
	if tmpl.DeployCount != 1 {
		t.Fatalf("expected deploy_count=1, got %d", tmpl.DeployCount)
	}
}

func TestIdeaAPIIncludesDeployCount(t *testing.T) {
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ideas", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/api/ideas: expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"deploy_count"`) {
		t.Fatalf("/api/ideas: expected deploy_count in JSON response")
	}
}
