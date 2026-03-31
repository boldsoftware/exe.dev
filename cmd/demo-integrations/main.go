package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	port := "8000"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	uiDist := "ui/dist"

	integrationsData := map[string]any{
		"integrations": []map[string]any{
			{"name": "my-api", "type": "http-proxy", "target": "https://api.example.com", "hasHeader": true, "hasBasicAuth": false, "repositories": []string{}, "attachments": []string{"tag:my-api", "vm:dev-box"}},
			{"name": "acme-org-backend", "type": "github", "target": "", "hasHeader": false, "hasBasicAuth": false, "repositories": []string{"acme-org/backend"}, "attachments": []string{"tag:acme-org-backend"}},
		},
		"githubIntegrations": []map[string]any{
			{"name": "acme-org-backend", "type": "github", "target": "", "hasHeader": false, "hasBasicAuth": false, "repositories": []string{"acme-org/backend"}, "attachments": []string{"tag:acme-org-backend"}},
		},
		"proxyIntegrations": []map[string]any{
			{"name": "my-api", "type": "http-proxy", "target": "https://api.example.com", "hasHeader": true, "hasBasicAuth": false, "repositories": []string{}, "attachments": []string{"tag:my-api", "vm:dev-box"}},
		},
		"githubAccounts": []map[string]any{
			{"githubLogin": "testuser", "targetLogin": "acme-org", "installationID": 12345},
		},
		"githubEnabled": true,
		"githubAppSlug": "exe-dev",
		"hasPushTokens": true,
		"allTags":       []string{"prod", "staging", "dev", "acme-org-backend", "my-api"},
		"tagVMs": map[string][]string{
			"prod":             {"web-server", "api-server", "worker-1", "worker-2", "db-server"},
			"staging":          {"staging-box", "staging-worker"},
			"dev":              {"dev-box"},
			"acme-org-backend": {"dev-box", "staging-box"},
			"my-api":           {"dev-box"},
		},
		"boxes": []map[string]string{
			{"name": "dev-box", "status": "running"},
			{"name": "web-server", "status": "running"},
			{"name": "api-server", "status": "running"},
			{"name": "worker-1", "status": "running"},
			{"name": "worker-2", "status": "stopped"},
			{"name": "db-server", "status": "running"},
			{"name": "staging-box", "status": "running"},
			{"name": "staging-worker", "status": "stopped"},
		},
		"integrationScheme": "https",
		"boxHost":           "demo.exe.dev",
	}

	dashboardData := map[string]any{
		"user": map[string]any{
			"email":                "demo@exe.dev",
			"region":               "us-east",
			"regionDisplay":        "US East",
			"newsletterSubscribed": false,
		},
		"boxes":             integrationsData["boxes"],
		"sharedBoxes":       []any{},
		"teamBoxes":         []any{},
		"inviteCount":       0,
		"canRequestInvites": false,
		"sshCommand":        "ssh demo@exe.dev",
		"replHost":          "demo.exe.dev",
		"showIntegrations":  true,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/integrations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(integrationsData)
	})

	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dashboardData)
	})

	mux.HandleFunc("/api/profile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"user":               dashboardData["user"],
			"sshKeys":            []any{},
			"passkeys":           []any{},
			"siteSessions":       []any{},
			"sharedBoxes":        []any{},
			"pendingTeamInvites": []any{},
			"canEnableTeam":      false,
			"credits":            map[string]any{"planName": "demo"},
			"basicUser":          false,
			"showIntegrations":   true,
			"inviteCount":        0,
			"canRequestInvites":  false,
			"boxes":              integrationsData["boxes"],
		})
	})

	mux.HandleFunc("/github/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"repos": []map[string]any{
				{"full_name": "acme-org/backend", "description": "Main backend service"},
				{"full_name": "acme-org/frontend", "description": "React frontend app"},
				{"full_name": "acme-org/infra", "description": "Infrastructure configs"},
				{"full_name": "testuser/dotfiles", "description": "Personal dotfiles"},
				{"full_name": "testuser/notes", "description": "Personal notes"},
			},
		})
	})

	mux.HandleFunc("/cmd", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Command string `json:"command"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		log.Printf("CMD: %s", req.Command)
		w.Header().Set("Content-Type", "application/json")

		if strings.HasPrefix(req.Command, "integrations add") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "output": fmt.Sprintf("Integration created")})
		} else if strings.HasPrefix(req.Command, "tag ") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "output": "Tag added"})
		} else if strings.HasPrefix(req.Command, "integrations attach") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "output": "Attached"})
		} else if strings.HasPrefix(req.Command, "integrations detach") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "output": "Detached"})
		} else if strings.HasPrefix(req.Command, "integrations remove") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "output": "Removed"})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "Unknown command: " + req.Command})
		}
	})

	fs := http.FileServer(http.Dir(uiDist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			http.ServeFile(w, r, uiDist+"/index.html")
			return
		}
		if _, err := os.Stat(uiDist + path); err == nil {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, uiDist+"/index.html")
	})

	log.Printf("Demo server listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
