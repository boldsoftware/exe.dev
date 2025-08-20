package exe

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// handleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches machine.team.exe.dev or machine.team.localhost
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	if !s.quietMode {
		slog.Info("[REDIRECT] handleProxyRequest called", "host", r.Host, "path", r.URL.Path)
	}
	// Handle magic URL for authentication
	if r.URL.Path == "/__exe.dev/auth" {
		s.handleMagicAuth(w, r)
		return
	}

	// Extract machine and team from Host header
	hostname := r.Host
	// Remove port if present
	if idx := strings.LastIndex(hostname, ":"); idx > 0 {
		hostname = hostname[:idx]
	}

	// Parse hostname to extract machine and team names
	machineName, teamName, err := s.parseProxyHostname(hostname)
	if err != nil {
		http.Error(w, "Invalid hostname format", http.StatusBadRequest)
		return
	}

	// Find the machine
	machine, err := s.getMachineByName(teamName, machineName)
	if err != nil {
		http.Error(w, "Machine not found", http.StatusNotFound)
		return
	}

	// Get the routes for the machine
	routes, err := machine.GetRoutes()
	if err != nil {
		http.Error(w, "Error loading routes", http.StatusInternalServerError)
		return
	}

	// Find matching route
	matchingRoute := s.findMatchingRoute(routes, r)
	if matchingRoute == nil {
		http.Error(w, "No matching route found", http.StatusNotFound)
		return
	}

	// Apply authentication based on route policy
	if matchingRoute.Policy == "private" {
		// Check if user is authenticated and has access to this team
		if !s.isUserAuthorizedForTeam(r, teamName) {
			// Redirect to authentication instead of returning 401
			s.redirectToAuth(w, r)
			return
		}
	}

	// For now, return debug info - will be replaced with actual proxy logic
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "Proxy handler - Route matched!\n")
	fmt.Fprintf(w, "Machine: %s.%s\n", machineName, teamName)
	fmt.Fprintf(w, "Matched route: %s (priority %d)\n", matchingRoute.Name, matchingRoute.Priority)
	fmt.Fprintf(w, "Policy: %s\n", matchingRoute.Policy)
	fmt.Fprintf(w, "Request method: %s\n", r.Method)
	fmt.Fprintf(w, "Request path: %s\n", r.URL.Path)

	// Show current user info
	if cookie, err := r.Cookie("exe-proxy-auth"); err == nil && cookie.Value != "" {
		if fingerprint, err := s.validateAuthCookie(cookie.Value, r.Host); err == nil {
			fmt.Fprintf(w, "Logged in user: %s\n", fingerprint)
		} else {
			fmt.Fprintf(w, "Invalid auth cookie: %v\n", err)
		}
	} else {
		fmt.Fprintf(w, "Not logged in\n")
	}
}

// isProxyRequest determines if a request should be handled by the proxy
func (s *Server) isProxyRequest(host string) bool {
	// Remove port if present
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostname = host[:idx]
	}

	// Check for production pattern: *.exe.dev (but not just exe.dev)
	if strings.HasSuffix(hostname, ".exe.dev") && hostname != "exe.dev" {
		return true
	}

	// Check for dev pattern: *.localhost (but not just localhost)
	if strings.HasSuffix(hostname, ".localhost") && hostname != "localhost" {
		return true
	}

	return false
}

// parseProxyHostname extracts machine and team names from hostname
// Supports both machine.team.exe.dev and machine.team.localhost formats
func (s *Server) parseProxyHostname(hostname string) (machine, team string, err error) {
	// Remove domain suffix based on dev mode
	expectedDomain := s.getMainDomain()
	expectedSuffix := "." + expectedDomain
	if strings.HasSuffix(hostname, expectedSuffix) {
		hostname = strings.TrimSuffix(hostname, expectedSuffix)
	} else {
		// Also support the other domain for flexibility
		if s.devMode != "" && strings.HasSuffix(hostname, ".exe.dev") {
			hostname = strings.TrimSuffix(hostname, ".exe.dev")
		} else if s.devMode == "" && strings.HasSuffix(hostname, ".localhost") {
			hostname = strings.TrimSuffix(hostname, ".localhost")
		} else {
			return "", "", fmt.Errorf("unsupported domain")
		}
	}

	// Split into machine.team
	parts := strings.Split(hostname, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("hostname must be in format machine.team")
	}

	return parts[0], parts[1], nil
}

// findMatchingRoute finds the best matching route for the request
// Routes are matched by priority (lower number = higher priority)
func (s *Server) findMatchingRoute(routes MachineRoutes, r *http.Request) *Route {
	// Sort routes by priority (lower number = higher priority)
	sortedRoutes := make(MachineRoutes, len(routes))
	copy(sortedRoutes, routes)

	// Use Go's sort package with secondary sort by name for consistent ordering
	sort.Slice(sortedRoutes, func(i, j int) bool {
		if sortedRoutes[i].Priority == sortedRoutes[j].Priority {
			return sortedRoutes[i].Name < sortedRoutes[j].Name
		}
		return sortedRoutes[i].Priority < sortedRoutes[j].Priority
	})

	for _, route := range sortedRoutes {
		if s.routeMatches(&route, r) {
			return &route
		}
	}

	return nil
}

// routeMatches checks if a route matches the request
func (s *Server) routeMatches(route *Route, r *http.Request) bool {
	// Check HTTP method
	methodMatches := false
	for _, method := range route.Methods {
		if method == "*" || method == r.Method {
			methodMatches = true
			break
		}
	}
	if !methodMatches {
		return false
	}

	// Check path prefix
	if !strings.HasPrefix(r.URL.Path, route.Paths.Prefix) {
		return false
	}

	return true
}

// isUserAuthorizedForTeam checks if the user is authorized to access the team
func (s *Server) isUserAuthorizedForTeam(r *http.Request, teamName string) bool {
	// Check for authentication cookie.
	// Slightly different name than the top-level cookie, just for ease of debugging.
	cookie, err := r.Cookie("exe-proxy-auth")
	if err != nil || cookie.Value == "" {
		return false
	}

	// Validate cookie and get user fingerprint
	fingerprint, err := s.validateAuthCookie(cookie.Value, r.Host)
	if err != nil {
		return false
	}

	// Check if user has access to this team
	hasAccess, err := s.userHasTeamAccess(fingerprint, teamName)
	if err != nil {
		return false
	}

	return hasAccess
}

// getMainDomain returns the main domain based on dev mode
func (s *Server) getMainDomain() string {
	if s.devMode != "" {
		return "localhost"
	}
	return "exe.dev"
}

// getMainDomainWithPort returns the main domain with port for redirects
func (s *Server) getMainDomainWithPort() string {
	if s.devMode != "" {
		// Extract port from httpAddr (e.g., ":8080" -> "8080")
		if s.httpAddr != "" {
			port := strings.TrimPrefix(s.httpAddr, ":")
			if port != "" {
				return fmt.Sprintf("localhost:%s", port)
			}
		}
		// Fallback to just localhost if no port specified
		return "localhost"
	}
	return "exe.dev"
}

// redirectToAuth redirects the user to authentication
func (s *Server) redirectToAuth(w http.ResponseWriter, r *http.Request) {
	// Create auth URL with redirect parameter
	authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))

	// If we're on a subdomain, redirect to the main domain
	if r.Host != "" {
		mainDomain := s.getMainDomainWithPort()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// Include the return host so we can come back after auth
		authURL = fmt.Sprintf("%s://%s%s&return_host=%s", scheme, mainDomain, authURL, url.QueryEscape(r.Host))
	}

	if !s.quietMode {
		slog.Info("[REDIRECT] redirectToAuth", "from", r.Host+r.URL.Path, "to", authURL)
	}
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleMagicAuth handles the magic authentication URL /__exe.dev/auth
// TODOX: rename this to indicate that this is for container/subdomain requests
func (s *Server) handleMagicAuth(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	redirectURL := r.URL.Query().Get("redirect")

	if !s.quietMode {
		slog.Info("[REDIRECT] handleMagicAuth called", "host", r.Host, "secret", secret[:10]+"...", "redirect", redirectURL)
	}

	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate and consume the magic secret
	magicSecret, err := s.validateMagicSecret(secret)
	if err != nil {
		if !s.quietMode {
			slog.Error("[REDIRECT] Magic secret validation failed", "error", err)
		}
		http.Error(w, "Invalid or expired secret", http.StatusUnauthorized)
		return
	}

	// Create authentication cookie for this subdomain
	cookieValue, err := s.createAuthCookie(magicSecret.Fingerprint, r.Host)
	if err != nil {
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	// Set the proxy auth cookie (different from main site cookie)
	cookie := &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
	}
	http.SetCookie(w, cookie)

	// Redirect to the original URL or the redirect from the magic secret
	finalRedirect := redirectURL
	if finalRedirect == "" {
		finalRedirect = magicSecret.RedirectURL
	}
	if finalRedirect == "" {
		finalRedirect = "/" // Default fallback
	}

	if !s.quietMode {
		slog.Info("[REDIRECT] handleMagicAuth redirecting", "to", finalRedirect)
	}
	http.Redirect(w, r, finalRedirect, http.StatusTemporaryRedirect)
}

// Route command handling methods

// handleRouteCommand handles route management commands from SSH
func (s *Server) handleRouteCommand(w io.Writer, fingerprint, teamName string, args []string) {
	// Define route help text
	routeHelpText := "\r\n\033[1;36mRoute subcommands:\033[0m\r\n\r\n" +
		"\033[1mroute <machine> list\033[0m           - List all routes\r\n" +
		"\033[1mroute <machine> add [flags]\033[0m    - Add a new route\r\n" +
		"\033[1mroute <machine> remove <name>\033[0m  - Remove a route by name\r\n\r\n" +
		"\033[1mAdd flags:\033[0m\r\n" +
		"  \033[1m--name=<name>\033[0m       Route name (auto-generated if not specified)\r\n" +
		"  \033[1m--priority=<num>\033[0m    Priority (auto-assigned if not specified)\r\n" +
		"  \033[1m--methods=<methods>\033[0m HTTP methods (default: '*' for all)\r\n" +
		"  \033[1m--prefix=<path>\033[0m     Path prefix to match (default: '/')\r\n" +
		"  \033[1m--policy=<policy>\033[0m   'public' or 'private' (default: 'private')\r\n" +
		"  \033[1m--ports=<ports>\033[0m     Allowed ports (default: '80,8000,8080,8888')\r\n\r\n"

	if len(args) < 2 {
		fmt.Fprintf(w, "\033[1;31mError: Please specify machine name and subcommand\033[0m\r\n")
		fmt.Fprint(w, routeHelpText)
		return
	}

	machineName := args[0]
	subCmd := args[1]
	subArgs := args[2:]

	switch subCmd {
	case "list":
		s.handleRouteList(w, fingerprint, teamName, machineName)
	case "add":
		s.handleRouteAdd(w, fingerprint, teamName, machineName, subArgs)
	case "remove":
		s.handleRouteRemove(w, fingerprint, teamName, machineName, subArgs)
	default:
		fmt.Fprintf(w, "\033[1;31mUnknown route command: %s\033[0m\r\n", subCmd)
		fmt.Fprint(w, routeHelpText)
	}
}

func (s *Server) handleRouteList(w io.Writer, fingerprint, teamName, machineName string) {
	// Get machine
	machine, err := s.getMachineForUser(fingerprint, teamName, machineName)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(w, "\r\n\033[1;36mRoutes for machine '%s':\033[0m\r\n\r\n", machineName)

	if len(routes) == 0 {
		fmt.Fprintf(w, "No routes configured.\r\n")
		return
	}

	for _, route := range routes {
		methods := strings.Join(route.Methods, ",")
		ports := make([]string, len(route.Ports))
		for i, port := range route.Ports {
			ports[i] = fmt.Sprintf("%d", port)
		}
		portList := strings.Join(ports, ",")

		fmt.Fprintf(w, "  \033[1m%s\033[0m (priority %d)\r\n", route.Name, route.Priority)
		fmt.Fprintf(w, "    Methods: %s\r\n", methods)
		fmt.Fprintf(w, "    Path prefix: %s\r\n", route.Paths.Prefix)
		fmt.Fprintf(w, "    Policy: %s\r\n", route.Policy)
		fmt.Fprintf(w, "    Ports: %s\r\n\r\n", portList)
	}
}

func (s *Server) handleRouteAdd(w io.Writer, fingerprint, teamName, machineName string, args []string) {
	// Create a FlagSet for parsing
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	var name, methodsStr, prefix, policy, portsStr string
	var priority int

	fs.StringVar(&name, "name", "", "route name (auto-generated if not specified)")
	fs.IntVar(&priority, "priority", -1, "priority (lower = higher priority, defaults to lowest priority)")
	fs.StringVar(&methodsStr, "methods", "*", "HTTP methods (comma-separated, or '*' for all)")
	fs.StringVar(&prefix, "prefix", "/", "path prefix to match")
	fs.StringVar(&policy, "policy", "private", "'public' or 'private'")
	fs.StringVar(&portsStr, "ports", "80,8000,8080,8888", "allowed ports (comma-separated)")

	// Capture the output to avoid printing errors to the session
	var buf bytes.Buffer
	fs.SetOutput(&buf)

	// Parse the flags
	err := fs.Parse(args)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError parsing flags: %v\033[0m\r\n", err)
		return
	}

	// Generate name if not provided
	if name == "" {
		name = s.generateRandomRouteName()
	}

	// Get machine
	machine, err := s.getMachineForUser(fingerprint, teamName, machineName)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get existing routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	// Set default priority if not specified
	if priority == -1 {
		// Find the highest priority number and set this route to be lower priority (higher number)
		maxPriority := 0
		for _, route := range routes {
			if route.Priority > maxPriority {
				maxPriority = route.Priority
			}
		}
		priority = maxPriority + 10 // Add some gap
	}

	// Parse methods
	var methods []string
	if methodsStr == "*" {
		methods = []string{"*"}
	} else {
		methods = strings.Split(methodsStr, ",")
		for i, method := range methods {
			methods[i] = strings.TrimSpace(strings.ToUpper(method))
		}
	}

	// Parse ports
	portStrs := strings.Split(portsStr, ",")
	var ports []int
	for _, portStr := range portStrs {
		portStr = strings.TrimSpace(portStr)
		port, err := strconv.Atoi(portStr)
		if err != nil {
			fmt.Fprintf(w, "\033[1;31mError: Invalid port '%s': %v\033[0m\r\n", portStr, err)
			return
		}
		ports = append(ports, port)
	}

	// Validate policy
	if policy != "public" && policy != "private" {
		fmt.Fprintf(w, "\033[1;31mError: Policy must be 'public' or 'private'\033[0m\r\n")
		return
	}

	// Check for duplicate name or priority
	for _, route := range routes {
		if route.Name == name {
			fmt.Fprintf(w, "\033[1;31mError: Route with name '%s' already exists\033[0m\r\n", name)
			return
		}
		if route.Priority == priority {
			fmt.Fprintf(w, "\033[1;31mError: Route with priority %d already exists\033[0m\r\n", priority)
			return
		}
	}

	// Create new route
	newRoute := Route{
		Name:     name,
		Priority: priority,
		Methods:  methods,
		Paths:    PathMatcher{Prefix: prefix},
		Policy:   policy,
		Ports:    ports,
	}

	// Add to routes list
	routes = append(routes, newRoute)

	// Set routes back on machine
	err = machine.SetRoutes(routes)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError encoding routes: %v\033[0m\r\n", err)
		return
	}

	// Update database
	_, err = s.db.Exec(`
		UPDATE machines SET routes = ? 
		WHERE name = ? AND team_name = ?`,
		*machine.Routes, machineName, teamName)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError saving route: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(w, "\033[1;32mRoute '%s' added successfully\033[0m\r\n", name)
}

func (s *Server) handleRouteRemove(w io.Writer, fingerprint, teamName, machineName string, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(w, "\033[1;31mError: Please specify route name\033[0m\r\n")
		fmt.Fprintf(w, "Usage: route %s remove <name>\r\n", machineName)
		return
	}

	routeName := args[0]

	// Get machine
	machine, err := s.getMachineForUser(fingerprint, teamName, machineName)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError: %v\033[0m\r\n", err)
		return
	}

	// Get existing routes
	routes, err := machine.GetRoutes()
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError parsing routes: %v\033[0m\r\n", err)
		return
	}

	// Find and remove the route
	var newRoutes MachineRoutes
	found := false
	for _, route := range routes {
		if route.Name == routeName {
			found = true
			continue // Skip this route (remove it)
		}
		newRoutes = append(newRoutes, route)
	}

	if !found {
		fmt.Fprintf(w, "\033[1;31mError: Route '%s' not found\033[0m\r\n", routeName)
		return
	}

	// Set routes back on machine
	err = machine.SetRoutes(newRoutes)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError encoding routes: %v\033[0m\r\n", err)
		return
	}

	// Update database
	_, err = s.db.Exec(`
		UPDATE machines SET routes = ? 
		WHERE name = ? AND team_name = ?`,
		*machine.Routes, machineName, teamName)
	if err != nil {
		fmt.Fprintf(w, "\033[1;31mError saving routes: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(w, "\033[1;32mRoute '%s' removed successfully\033[0m\r\n", routeName)
}

// getMachineForUser retrieves a machine for the given user/team/name
func (s *Server) getMachineForUser(fingerprint, teamName, machineName string) (*Machine, error) {
	// First verify user has access to the team
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM team_members 
			WHERE user_fingerprint = ? AND team_name = ?
		)`, fingerprint, teamName).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("database error: %v", err)
	}
	if !exists {
		return nil, fmt.Errorf("access denied to team '%s'", teamName)
	}

	// Get the machine
	var machine Machine
	err = s.db.QueryRow(`
		SELECT id, team_name, name, status, image, container_id, 
		       created_by_fingerprint, created_at, updated_at, 
		       last_started_at, docker_host, routes
		FROM machines 
		WHERE name = ? AND team_name = ?`, machineName, teamName).Scan(
		&machine.ID, &machine.TeamName, &machine.Name, &machine.Status,
		&machine.Image, &machine.ContainerID, &machine.CreatedByFingerprint,
		&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt,
		&machine.DockerHost, &machine.Routes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("machine '%s' not found", machineName)
		}
		return nil, fmt.Errorf("database error: %v", err)
	}

	return &machine, nil
}

// generateRandomRouteName generates a random route name using famous roads
func (s *Server) generateRandomRouteName() string {
	roads := []string{
		"abbey", "baker", "broadway", "canal", "champs", "downing", "embassy", "fleet",
		"grand", "hollywood", "imperial", "kings", "lombard", "madison", "oxford", "piccadilly",
		"regent", "sunset", "times", "victoria", "wall", "westminster", "bourbon", "castro",
		"fifth", "harvard", "melrose", "newbury", "rodeo", "ventura", "wilshire", "sunset",
		"michigan", "pennsylvania", "massachusetts", "connecticut", "california", "florida",
		"bourbon", "royal", "chartres", "dauphine", "magazine", "esplanade", "canal", "poydras",
	}

	road1 := roads[mathrand.Intn(len(roads))]
	road2 := roads[mathrand.Intn(len(roads))]

	// Avoid duplicates in the same name
	for road2 == road1 {
		road2 = roads[mathrand.Intn(len(roads))]
	}

	return fmt.Sprintf("%s-%s", road1, road2)
}
