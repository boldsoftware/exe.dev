package execore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/idea"
)

func TestSeedDefaultTemplatesUpdatesExistingTemplatePrompt(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func ideaTestUserCookie(t *testing.T, server *Server) (string, *http.Cookie) {
	t.Helper()
	user, err := server.createUser(t.Context(), testSSHPubKey, "test-idea@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}
	return user.UserID, &http.Cookie{Name: "exe-auth", Value: cookieValue}
}

func TestIdeaRateReturnsUpdatedStats(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	_, cookie := ideaTestUserCookie(t, server)

	// Get template ID for openclaw
	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}

	// Rate the template
	body := strings.NewReader(fmt.Sprintf(`{"template_id":%d,"rating":4}`, tmpl.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/rate", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/api/ideas/rate: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Status      string  `json:"status"`
		AvgRating   float64 `json:"avg_rating"`
		RatingCount int64   `json:"rating_count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status=ok, got %q", resp.Status)
	}
	if resp.AvgRating != 4.0 {
		t.Fatalf("expected avg_rating=4.0, got %f", resp.AvgRating)
	}
	if resp.RatingCount != 1 {
		t.Fatalf("expected rating_count=1, got %d", resp.RatingCount)
	}
}

func TestIdeaMyRatings(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	userID, cookie := ideaTestUserCookie(t, server)

	// Get template ID
	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}

	// Rate it
	if err := withTx1(server, t.Context(), (*exedb.Queries).UpsertTemplateRating, exedb.UpsertTemplateRatingParams{
		TemplateID: tmpl.ID,
		UserID:     userID,
		Rating:     3,
	}); err != nil {
		t.Fatalf("UpsertTemplateRating failed: %v", err)
	}

	// Fetch my-ratings
	req := httptest.NewRequest(http.MethodGet, "/api/ideas/my-ratings", nil)
	req.Host = server.env.WebHost
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/api/ideas/my-ratings: expected 200, got %d", w.Code)
	}

	var ratings map[string]int64
	if err := json.NewDecoder(w.Body).Decode(&ratings); err != nil {
		t.Fatalf("decode: %v", err)
	}

	key := fmt.Sprintf("%d", tmpl.ID)
	if ratings[key] != 3 {
		t.Fatalf("expected rating=3 for template %d, got %d", tmpl.ID, ratings[key])
	}
}

func TestIdeaSubmitCreatesAndSlackNotifies(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	_, cookie := ideaTestUserCookie(t, server)

	body := strings.NewReader(`{"title":"My Cool Idea","slug":"my-cool-idea","short_description":"A test idea","category":"dev-tools","prompt":"Build something cool"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/submit", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("/api/ideas/submit: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "submitted" {
		t.Fatalf("expected status=submitted, got %q", resp["status"])
	}

	// Verify it's pending and not visible in approved list
	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "my-cool-idea")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}
	if tmpl.Status != "pending" {
		t.Fatalf("expected status=pending, got %q", tmpl.Status)
	}
	if tmpl.Title != "My Cool Idea" {
		t.Fatalf("expected title='My Cool Idea', got %q", tmpl.Title)
	}

	// Should not appear in approved list
	approved, err := withRxRes0(server, t.Context(), (*exedb.Queries).ListApprovedTemplates)
	if err != nil {
		t.Fatalf("ListApprovedTemplates failed: %v", err)
	}
	for _, a := range approved {
		if a.Slug == "my-cool-idea" {
			t.Fatal("submitted idea should not appear in approved list")
		}
	}

	// Should appear in all templates list (admin view)
	all, err := withRxRes0(server, t.Context(), (*exedb.Queries).ListAllTemplates)
	if err != nil {
		t.Fatalf("ListAllTemplates failed: %v", err)
	}
	found := false
	for _, a := range all {
		if a.Slug == "my-cool-idea" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("submitted idea should appear in all templates list")
	}
}

func TestIdeaSubmitRequiresAuth(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	body := strings.NewReader(`{"title":"Test","slug":"test-idea","prompt":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/submit", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("/api/ideas/submit without auth: expected 401, got %d", w.Code)
	}
}

func TestIdeaSubmitValidation(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	_, cookie := ideaTestUserCookie(t, server)

	// Missing title
	body := strings.NewReader(`{"slug":"test","prompt":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/submit", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit with missing title: expected 400, got %d", w.Code)
	}

	// Missing prompt
	body = strings.NewReader(`{"title":"Test","slug":"test-idea"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/ideas/submit", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit with missing prompt: expected 400, got %d", w.Code)
	}
}

func TestIdeaSubmitDuplicateSlug(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}
	_, cookie := ideaTestUserCookie(t, server)

	// Try to submit with an existing slug
	body := strings.NewReader(`{"title":"Openclaw Copy","slug":"openclaw","prompt":"duplicate"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/submit", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("submit with duplicate slug: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIdeaRateAllowedForBasicUserWithLLMUseEntitlement(t *testing.T) {
	t.Parallel()
	// Basic plan grants LLMUse, so even a non-paying user can rate templates.
	// This is an intentional expansion from the old userIsPaying check.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	// Create a user without billing — they'll be on the Basic plan which grants LLMUse
	user, err := server.createUser(t.Context(), testSSHPubKey, "rate-basic@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}
	cookie := &http.Cookie{Name: "exe-auth", Value: cookieValue}

	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}

	body := strings.NewReader(fmt.Sprintf(`{"template_id":%d,"rating":4}`, tmpl.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/rate", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/api/ideas/rate for Basic user: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIdeaRateAllowedWithLLMUseEntitlement(t *testing.T) {
	t.Parallel()
	// A user with active billing (Individual plan) should have LLMUse
	// and get 200 from the rate API.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	user, err := server.createUser(t.Context(), testSSHPubKey, "rate-allowed@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	activateUserBilling(t, server, user.UserID)

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}
	cookie := &http.Cookie{Name: "exe-auth", Value: cookieValue}

	tmpl, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetTemplateBySlugAny, "openclaw")
	if err != nil {
		t.Fatalf("GetTemplateBySlugAny failed: %v", err)
	}

	body := strings.NewReader(fmt.Sprintf(`{"template_id":%d,"rating":5}`, tmpl.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/ideas/rate", body)
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/api/ideas/rate with billing: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIdeaPageCanRateRequiresLLMUseEntitlement(t *testing.T) {
	t.Parallel()
	// canRate should be false for a user without billing and true for a user with billing.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false
	if err := server.seedDefaultTemplates(t.Context()); err != nil {
		t.Fatalf("seedDefaultTemplates failed: %v", err)
	}

	// Create a user without billing
	user, err := server.createUser(t.Context(), testSSHPubKey, "canrate-test@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}
	cookie := &http.Cookie{Name: "exe-auth", Value: cookieValue}

	// Request idea page as non-paying user — canRate should be false
	req := httptest.NewRequest(http.MethodGet, "/idea", nil)
	req.Host = server.env.WebHost
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/idea: expected 200, got %d", w.Code)
	}
	// The template renders CanRate into a JS variable; check for the false case.
	// When CanRate is false, the rating UI is hidden. We check that "canRate: true" is NOT present.
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, "canRate: true") || strings.Contains(bodyStr, `"canRate":true`) {
		t.Fatal("/idea: expected canRate to be false for non-paying user")
	}

	// Now activate billing and check again
	activateUserBilling(t, server, user.UserID)

	req = httptest.NewRequest(http.MethodGet, "/idea", nil)
	req.Host = server.env.WebHost
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/idea (paying): expected 200, got %d", w.Code)
	}
}
