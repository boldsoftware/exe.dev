package exe

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exe.dev/exedb"
)

// handleMobile handles the mobile UI flow at /m using a mux for cleaner routing
func (s *Server) handleMobile(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleMobileHome)
	mux.HandleFunc("/check-hostname", s.handleMobileHostnameCheck)
	mux.HandleFunc("/create-vm", s.handleMobileCreateVM)
	mux.HandleFunc("/email-auth", s.handleMobileEmailAuth)
	mux.HandleFunc("/verify-code", s.handleMobileVerifyCode)
	mux.HandleFunc("/home", s.handleMobileVMList)

	// Strip /m prefix before passing to mux
	originalURL := r.URL.Path
	r.URL.Path = strings.TrimPrefix(originalURL, "/m")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	mux.ServeHTTP(w, r)

	// Restore original URL path
	r.URL.Path = originalURL
}

// handleMobileHome renders the initial mobile page
func (s *Server) handleMobileHome(w http.ResponseWriter, r *http.Request) {
	// Check if user is already authenticated
	if cookie, err := r.Cookie("exe-auth"); err == nil && cookie.Value != "" {
		if _, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err == nil {
			// User is authenticated, redirect to mobile home
			http.Redirect(w, r, "/m/home", http.StatusTemporaryRedirect)
			return
		}
	}

	// Generate a random hostname suggestion
	hostnameSuggestion := generateRandomBoxName()

	data := struct {
		HostnameSuggestion string
	}{
		HostnameSuggestion: hostnameSuggestion,
	}

	s.renderTemplate(w, "mobile-home.html", data)
}

// handleMobileHostnameCheck checks if a hostname is available
func (s *Server) handleMobileHostnameCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		Hostname string `json:"hostname"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	hostname := strings.TrimSpace(request.Hostname)
	if hostname == "" {
		http.Error(w, "Hostname is required", http.StatusBadRequest)
		return
	}

	// Check if hostname is valid and available
	isValid := s.isValidBoxName(hostname)
	isAvailable := true

	if isValid {
		// Check if hostname is already taken
		box, err := s.getBoxByName(r.Context(), hostname)
		if err == nil && box != nil {
			isAvailable = false
		}
	}

	response := struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}{
		Valid:     isValid,
		Available: isAvailable,
	}

	if !isValid {
		response.Message = "Invalid hostname format. Must be 5-64 characters, letters, numbers, and hyphens only."
	} else if !isAvailable {
		response.Message = "This hostname is already taken. Please choose another."
	} else {
		response.Message = "This hostname is available!"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleMobileCreateVM handles VM creation request
func (s *Server) handleMobileCreateVM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := strings.TrimSpace(r.FormValue("hostname"))
	description := strings.TrimSpace(r.FormValue("description"))

	slog.Info("Mobile VM creation request", "hostname", hostname, "description", description)

	// For now, we just log the request and proceed to email auth
	// Store the request in session or pass via URL parameters
	data := struct {
		Hostname    string
		Description string
	}{
		Hostname:    hostname,
		Description: description,
	}

	s.renderTemplate(w, "mobile-email-auth.html", data)
}

// handleMobileEmailAuth handles email authentication
func (s *Server) handleMobileEmailAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}

	// Basic email validation
	if !s.isValidEmail(email) {
		http.Error(w, "Invalid email format", http.StatusBadRequest)
		return
	}

	// Generate verification token
	token := s.generateToken()

	// Store email verification token in database
	err := s.storeEmailVerification(r.Context(), email, token)
	if err != nil {
		slog.Error("Failed to store email verification", "error", err)
		http.Error(w, "Failed to process request", http.StatusInternalServerError)
		return
	}

	// Send verification email
	verifyURL := fmt.Sprintf("%s/m/verify-code?token=%s", s.getBaseURL(), token)
	subject := "Verify your email - exe.dev"
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email and complete your setup:

%s

Or use this verification code: %s

This link will expire in 24 hours.

Best regards,
The exe.dev team`, verifyURL, token[:8])

	err = s.sendEmail(email, subject, body)
	if err != nil {
		slog.Error("Failed to send verification email", "error", err)
		http.Error(w, "Failed to send email", http.StatusInternalServerError)
		return
	}

	data := struct {
		Email  string
		DevURL string
		Code   string
	}{
		Email: email,
		Code:  token[:8],
	}

	if s.devMode != "" {
		data.DevURL = verifyURL
	}

	s.renderTemplate(w, "mobile-email-sent.html", data)
}

// handleMobileVerifyCode handles code verification
func (s *Server) handleMobileVerifyCode(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Handle email link click
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token", http.StatusBadRequest)
			return
		}

		userID, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			http.Error(w, "Invalid or expired token", http.StatusBadRequest)
			return
		}

		// Create auth cookie
		cookieValue, err := s.createAuthCookie(r.Context(), userID, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie", "error", err)
			http.Error(w, "Authentication failed", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "exe-auth",
			Value:    cookieValue,
			Path:     "/",
			Expires:  time.Now().Add(30 * 24 * time.Hour),
			Secure:   s.devMode == "", // Only secure in production
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/m/home", http.StatusTemporaryRedirect)
		return
	}

	if r.Method == http.MethodPost {
		// Handle manual code entry
		code := strings.TrimSpace(r.FormValue("code"))
		if code == "" {
			http.Error(w, "Code is required", http.StatusBadRequest)
			return
		}

		// Find full token by code prefix
		userID, err := s.validateEmailVerificationByCode(r.Context(), code)
		if err != nil {
			http.Error(w, "Invalid or expired code", http.StatusBadRequest)
			return
		}

		// Create auth cookie
		cookieValue, err := s.createAuthCookie(r.Context(), userID, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie", "error", err)
			http.Error(w, "Authentication failed", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "exe-auth",
			Value:    cookieValue,
			Path:     "/",
			Expires:  time.Now().Add(30 * 24 * time.Hour),
			Secure:   s.devMode == "", // Only secure in production
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/m/home", http.StatusTemporaryRedirect)
		return
	}

	// GET request to show verification form
	data := struct {
		Error string
	}{}

	s.renderTemplate(w, "mobile-verify-code.html", data)
}

// handleMobileVMList shows the user's VM list
func (s *Server) handleMobileVMList(w http.ResponseWriter, r *http.Request) {
	// Check authentication
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}

	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
		http.Redirect(w, r, "/m", http.StatusTemporaryRedirect)
		return
	}

	// Get user's allocation
	alloc, err := s.getUserAlloc(r.Context(), userID)
	if err != nil {
		slog.Error("Failed to get user allocation", "error", err, "user_id", userID)
		http.Error(w, "Failed to load user data", http.StatusInternalServerError)
		return
	}

	// Get user's boxes
	var boxes []exedb.Box
	if alloc != nil {
		boxes, err = s.getBoxesForAlloc(r.Context(), alloc.AllocID)
		if err != nil {
			slog.Error("Failed to get boxes", "error", err, "alloc_id", alloc.AllocID)
			http.Error(w, "Failed to load boxes", http.StatusInternalServerError)
			return
		}
	}

	// Convert []exedb.Box to []*exedb.Box
	boxPtrs := make([]*exedb.Box, len(boxes))
	for i := range boxes {
		boxPtrs[i] = &boxes[i]
	}

	data := struct {
		Boxes []*exedb.Box
	}{
		Boxes: boxPtrs,
	}

	s.renderTemplate(w, "mobile-vm-list.html", data)
}

// Helper functions for mobile
