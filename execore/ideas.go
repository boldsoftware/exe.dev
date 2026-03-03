package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"exe.dev/execore/debug_templates"
	"exe.dev/exedb"
	"exe.dev/idea"
	"exe.dev/stage"
)

// handleTemplatesAPI serves GET /api/ideas — returns approved templates as JSON.
func (s *Server) handleTemplatesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	templates, err := withRxRes0(s, r.Context(), (*exedb.Queries).ListApprovedTemplates)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to list templates", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	out := make([]idea.JSON, len(templates))
	for i, t := range templates {
		out[i] = idea.ApprovedRowToJSON(t)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleTemplateRateAPI handles POST /api/ideas/rate.
// Body: {"template_id": 1, "rating": 5}
// Requires authenticated user with active billing.
func (s *Server) handleTemplateRateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Check billing status
	if !s.env.SkipBilling {
		status, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID)
		if err != nil || !userIsPaying(&status) {
			http.Error(w, "Active billing required to rate templates", http.StatusForbidden)
			return
		}
	}

	var req struct {
		TemplateID int64 `json:"template_id"`
		Rating     int64 `json:"rating"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		http.Error(w, "Rating must be 1-5", http.StatusBadRequest)
		return
	}

	err = withTx1(s, r.Context(), (*exedb.Queries).UpsertTemplateRating, exedb.UpsertTemplateRatingParams{
		TemplateID: req.TemplateID,
		UserID:     userID,
		Rating:     req.Rating,
	})
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to upsert rating", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Return updated rating stats so the client can update the UI immediately.
	stats, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetTemplateRatingStats, req.TemplateID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to get rating stats", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Status      string  `json:"status"`
		AvgRating   float64 `json:"avg_rating"`
		RatingCount int64   `json:"rating_count"`
	}{
		Status:      "ok",
		AvgRating:   stats.AvgRating,
		RatingCount: stats.RatingCount,
	})
}

// handleMyRatingsAPI serves GET /api/ideas/my-ratings — returns the logged-in user's ratings.
func (s *Server) handleMyRatingsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := s.validateAuthCookie(r)
	if err != nil {
		// Not logged in — return empty object.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}

	ratings, err := withRxRes1(s, r.Context(), (*exedb.Queries).ListUserTemplateRatings, userID)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "Failed to list user ratings", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Return as {template_id: rating} map.
	out := make(map[int64]int64, len(ratings))
	for _, r := range ratings {
		out[r.TemplateID] = r.Rating
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleTemplateSubmitAPI handles POST /api/ideas/submit.
// Body: {"title": "...", "slug": "...", "short_description": "...", "category": "...", "prompt": "..."}
// Requires authenticated user.
func (s *Server) handleTemplateSubmitAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	var req struct {
		Title            string `json:"title"`
		Slug             string `json:"slug"`
		ShortDescription string `json:"short_description"`
		Category         string `json:"category"`
		Prompt           string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	req.ShortDescription = strings.TrimSpace(req.ShortDescription)
	req.Category = strings.TrimSpace(req.Category)
	req.Prompt = strings.TrimSpace(req.Prompt)

	if req.Title == "" || req.Slug == "" || req.Prompt == "" {
		http.Error(w, "Title, slug, and prompt are required", http.StatusBadRequest)
		return
	}
	if !idea.ValidSlugRe.MatchString(req.Slug) {
		http.Error(w, "Slug must be 3-64 lowercase alphanumeric characters and hyphens", http.StatusBadRequest)
		return
	}
	if len(req.Title) > 100 {
		http.Error(w, "Title must be under 100 characters", http.StatusBadRequest)
		return
	}
	if len(req.ShortDescription) > 300 {
		http.Error(w, "Description must be under 300 characters", http.StatusBadRequest)
		return
	}
	if len(req.Prompt) > 5000 {
		http.Error(w, "Prompt must be under 5000 characters", http.StatusBadRequest)
		return
	}

	if !idea.ValidCategory(req.Category) {
		req.Category = "other"
	}

	err = withTx1(s, r.Context(), (*exedb.Queries).InsertTemplate, exedb.InsertTemplateParams{
		Slug:             req.Slug,
		Title:            req.Title,
		ShortDescription: req.ShortDescription,
		Category:         req.Category,
		Prompt:           req.Prompt,
		AuthorUserID:     &userID,
		Status:           "pending",
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			http.Error(w, "A template with that slug already exists", http.StatusConflict)
			return
		}
		s.slog().ErrorContext(r.Context(), "Failed to insert template", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "submitted"})
}

// handleIdeaPage renders the /idea browsing page.
func (s *Server) handleIdeaPage(w http.ResponseWriter, r *http.Request) {
	userID, err := s.validateAuthCookie(r)
	isLoggedIn := err == nil

	canRate := false
	if isLoggedIn && !s.env.SkipBilling {
		if status, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID); err == nil && userIsPaying(&status) {
			canRate = true
		}
	} else if isLoggedIn && s.env.SkipBilling {
		canRate = true
	}

	data := struct {
		stage.Env
		IsLoggedIn bool
		ActivePage string
		BasicUser  bool
		CanRate    bool
	}{
		Env:        s.env,
		IsLoggedIn: isLoggedIn,
		ActivePage: "",
		BasicUser:  false,
		CanRate:    canRate,
	}
	s.renderTemplate(r.Context(), w, "idea.html", data)
}

// handleDebugTemplateReview renders the template review admin page.
func (s *Server) handleDebugTemplateReview(w http.ResponseWriter, r *http.Request) {
	templates, err := withRxRes0(s, r.Context(), (*exedb.Queries).ListAllTemplates)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list templates: %v", err), http.StatusInternalServerError)
		return
	}

	type tmplData struct {
		ID               int64
		Slug             string
		Title            string
		ShortDescription string
		Category         string
		Prompt           string
		IconURL          string
		ScreenshotURL    string
		AuthorUserID     string
		Status           string
		Featured         bool
		AvgRating        float64
		RatingCount      int64
		VMShortname      string
		Image            string
	}

	data := struct {
		Templates  []tmplData
		Categories []idea.Category
	}{
		Categories: idea.Categories,
	}

	for _, t := range templates {
		author := ""
		if t.AuthorUserID != nil {
			author = *t.AuthorUserID
		}
		j := idea.AllRowToJSON(t)
		data.Templates = append(data.Templates, tmplData{
			ID:               j.ID,
			Slug:             j.Slug,
			Title:            j.Title,
			ShortDescription: j.ShortDescription,
			Category:         j.Category,
			Prompt:           j.Prompt,
			IconURL:          j.IconURL,
			ScreenshotURL:    j.ScreenshotURL,
			AuthorUserID:     author,
			Status:           t.Status,
			Featured:         j.Featured,
			AvgRating:        j.AvgRating,
			RatingCount:      j.RatingCount,
			VMShortname:      j.VMShortname,
			Image:            j.Image,
		})
	}

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "template-review.html", data); err != nil {
		http.Error(w, fmt.Sprintf("failed to render: %v", err), http.StatusInternalServerError)
	}
}

// handleDebugTemplateReviewPost handles POST actions on template review.
func (s *Server) handleDebugTemplateReviewPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")

	// Seed doesn't need an ID.
	if action == "seed" {
		if err := s.seedDefaultTemplates(r.Context()); err != nil {
			http.Error(w, fmt.Sprintf("Action failed: %v", err), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/debug/ideas", http.StatusSeeOther)
		return
	}

	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid template ID", http.StatusBadRequest)
		return
	}

	switch action {
	case "approve":
		err = withTx1(s, r.Context(), (*exedb.Queries).UpdateTemplateStatus, exedb.UpdateTemplateStatusParams{
			Status: "approved",
			ID:     id,
		})
	case "reject":
		err = withTx1(s, r.Context(), (*exedb.Queries).UpdateTemplateStatus, exedb.UpdateTemplateStatusParams{
			Status: "rejected",
			ID:     id,
		})
	case "delete":
		err = withTx1(s, r.Context(), (*exedb.Queries).DeleteTemplate, id)
	case "update":
		featured := r.FormValue("featured") == "true"
		err = withTx1(s, r.Context(), (*exedb.Queries).UpdateTemplate, exedb.UpdateTemplateParams{
			ID:               id,
			Title:            r.FormValue("title"),
			ShortDescription: r.FormValue("short_description"),
			Category:         r.FormValue("category"),
			Prompt:           r.FormValue("prompt"),
			IconURL:          r.FormValue("icon_url"),
			ScreenshotURL:    r.FormValue("screenshot_url"),
			Featured:         featured,
			VMShortname:      r.FormValue("vm_shortname"),
			Image:            r.FormValue("image"),
		})
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("Action failed: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/debug/ideas", http.StatusSeeOther)
}

// seedDefaultTemplates inserts the curated set of idea templates.
func (s *Server) seedDefaultTemplates(ctx context.Context) error {
	for _, t := range idea.SeedTemplates {
		existing, err := withRxRes1(s, ctx, (*exedb.Queries).GetTemplateBySlugAny, t.Slug)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("checking template %s: %w", t.Slug, err)
			}
			if err := withTx1(s, ctx, (*exedb.Queries).InsertTemplate, t); err != nil {
				return fmt.Errorf("seeding template %s: %w", t.Slug, err)
			}
			continue
		}

		if err := withTx1(s, ctx, (*exedb.Queries).UpdateTemplate, exedb.UpdateTemplateParams{
			ID:               existing.ID,
			Title:            t.Title,
			ShortDescription: t.ShortDescription,
			Category:         t.Category,
			Prompt:           t.Prompt,
			IconURL:          t.IconURL,
			ScreenshotURL:    t.ScreenshotURL,
			Featured:         t.Featured,
			VMShortname:      t.VMShortname,
			Image:            t.Image,
		}); err != nil {
			return fmt.Errorf("updating template %s: %w", t.Slug, err)
		}
		if err := withTx1(s, ctx, (*exedb.Queries).UpdateTemplateStatus, exedb.UpdateTemplateStatusParams{
			ID:     existing.ID,
			Status: t.Status,
		}); err != nil {
			return fmt.Errorf("updating template status %s: %w", t.Slug, err)
		}
	}
	return nil
}
