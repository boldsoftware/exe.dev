package execore

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/xshelley"
)

// handleShelleyDownload handles requests for downloading the shelley binary
func (s *Server) handleShelleyDownload(w http.ResponseWriter, r *http.Request) {
	arch := r.URL.Query().Get("arch")

	// If arch not specified, show HTML page with architecture options
	if arch == "" {
		s.renderShelleyDownloadPage(w, r)
		return
	}

	// Validate architecture
	var goarch string
	switch arch {
	case "linux-amd64", "amd64":
		goarch = "amd64"
	case "linux-arm64", "arm64":
		goarch = "arm64"
	default:
		http.Error(w, "Unsupported architecture. Supported: linux-amd64, linux-arm64", http.StatusBadRequest)
		return
	}

	// Get the shelley binary using xshelley
	ctx := r.Context()
	shelleyPath, err := getShelleyBinary(ctx, goarch)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to get shelley binary", "error", err, "goarch", goarch)
		http.Error(w, "Failed to retrieve shelley binary", http.StatusInternalServerError)
		return
	}

	// Serve the binary file
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=shelley-linux-%s", goarch))

	http.ServeFile(w, r, shelleyPath)
}

// renderShelleyDownloadPage renders an HTML page with architecture selection
func (s *Server) renderShelleyDownloadPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	html := `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Download Shelley</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            max-width: 600px;
            margin: 50px auto;
            padding: 20px;
            line-height: 1.6;
        }
        h1 {
            color: #333;
        }
        .download-links {
            margin-top: 30px;
        }
        .download-link {
            display: block;
            padding: 15px 20px;
            margin: 10px 0;
            background-color: #f4f4f4;
            color: #333;
            text-decoration: none;
            border-radius: 5px;
            transition: background-color 0.2s;
        }
        .download-link:hover {
            background-color: #e4e4e4;
        }
        .arch-name {
            font-weight: bold;
            font-size: 1.1em;
        }
        .arch-desc {
            font-size: 0.9em;
            color: #666;
            margin-top: 5px;
        }
    </style>
</head>
<body>
    <h1>Download Shelley</h1>
    <p>Select your architecture to download the shelley binary:</p>

    <div class="download-links">
        <a href="/shelley/download?arch=linux-amd64" class="download-link">
            <div class="arch-name">Linux AMD64 (x86_64)</div>
            <div class="arch-desc">For Intel and AMD processors</div>
        </a>

        <a href="/shelley/download?arch=linux-arm64" class="download-link">
            <div class="arch-name">Linux ARM64 (aarch64)</div>
            <div class="arch-desc">For ARM processors (e.g., Raspberry Pi, AWS Graviton)</div>
        </a>
    </div>
</body>
</html>`

	fmt.Fprint(w, html)
}

// getShelleyBinary is a wrapper that calls xshelley.GetShelley
func getShelleyBinary(ctx context.Context, goarch string) (string, error) {
	return xshelley.GetShelley(ctx, goarch)
}
