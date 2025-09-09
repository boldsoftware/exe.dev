package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed asciinema-player.css
var cssContent string

//go:embed asciinema-player.min.js
var jsContent string

type Recording struct {
	Name     string
	Title    string
	Filename string
	Content  string // base64 encoded .cast content
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: asciinema-viewer <recordings-dir> <output-file>")
	}

	recordingsDir := os.Args[1]
	outputFile := os.Args[2]

	recordings, err := loadRecordings(recordingsDir)
	if err != nil {
		log.Fatalf("Failed to load recordings: %v", err)
	}

	html := generateHTML(recordings, cssContent, jsContent)

	if err := os.WriteFile(outputFile, []byte(html), 0o644); err != nil {
		log.Fatalf("Failed to write output file: %v", err)
	}

	fmt.Printf("Generated viewer with %d recordings: %s\n", len(recordings), outputFile)
}

func loadRecordings(dir string) ([]Recording, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.cast"))
	if err != nil {
		return nil, err
	}

	var recordings []Recording
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			log.Printf("Warning: failed to read %s: %v", file, err)
			continue
		}

		name := filepath.Base(file)
		title := formatTitle(strings.TrimSuffix(name, ".cast"))

		recordings = append(recordings, Recording{
			Name:     name,
			Title:    title,
			Filename: name,
			Content:  base64.StdEncoding.EncodeToString(content),
		})
	}

	// Sort by title for consistent ordering
	sort.Slice(recordings, func(i, j int) bool {
		return recordings[i].Title < recordings[j].Title
	})

	return recordings, nil
}

func formatTitle(name string) string {
	// Convert TestSSHConnection -> SSH Connection
	title := strings.ReplaceAll(name, "Test", "")
	title = strings.ReplaceAll(title, "_", " ")

	// Add spaces before capital letters
	var result strings.Builder
	for i, r := range title {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune(' ')
		}
		result.WriteRune(r)
	}

	return strings.TrimSpace(result.String())
}

func generateHTML(recordings []Recording, cssContent, jsContent string) string {
	tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>exe.dev E2E Test Recordings</title>
    <style>
        {{.CSS}}

        * { margin: 0; padding: 0; box-sizing: border-box; }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', Arial, sans-serif;
            height: 100vh;
            display: flex;
            background: #f5f5f5;
        }


        .sidebar {
            width: 300px;
            background: white;
            border-right: 1px solid #e1e5e9;
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .sidebar-header {
            padding: 20px;
            border-bottom: 1px solid #e1e5e9;
            background: #f8f9fa;
        }

        .sidebar-header h1 {
            font-size: 18px;
            color: #24292e;
            margin-bottom: 5px;
        }

        .sidebar-header p {
            font-size: 14px;
            color: #586069;
        }

        .recording-list {
            flex: 1;
            overflow-y: auto;
        }

        .recording-item {
            display: block;
            padding: 12px 20px;
            color: #24292e;
            text-decoration: none;
            border-bottom: 1px solid #f1f3f4;
            transition: background-color 0.15s ease;
            cursor: pointer;
        }

        .recording-item:hover {
            background-color: #f6f8fa;
        }

        .recording-item.active {
            background-color: #0366d6;
            color: white;
        }

        .recording-item.active:hover {
            background-color: #0256cc;
        }

        .main-content {
            flex: 1;
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .player-container {
            flex: 1;
            display: flex;
            justify-content: center;
            align-items: flex-start;
            padding: 10px;
            background: white;
            overflow: auto;
        }

        #player-target {
            width: 100%;
            max-width: 100%;
            overflow: hidden;
        }

        /* Allow natural player sizing */
        .ap-wrapper {
            margin: 0 auto;
        }

        .ap-player {
            /* Let the player size itself naturally */
        }

        .empty-state {
            text-align: center;
            color: #586069;
        }

        .empty-state h3 {
            font-size: 16px;
            margin-bottom: 8px;
        }

        asciinema-player {
            border-radius: 6px;
            box-shadow: 0 8px 24px rgba(140, 149, 159, 0.2);
        }

        @media (max-width: 768px) {
            body { flex-direction: column; }
            .sidebar { width: 100%; height: 200px; }
            .recording-list { flex-direction: row; overflow-x: auto; }
            .recording-item { min-width: 150px; }
        }
    </style>
</head>
<body>
    <div class="sidebar">
        <div class="sidebar-header">
            <h1>exe.dev Recordings</h1>
            <p>E2E test demonstrations</p>
        </div>
        <div class="recording-list">
            {{range $i, $recording := .Recordings}}
            <a href="#" class="recording-item{{if eq $i 0}} active{{end}}" data-recording="{{$recording.Name}}">
                {{$recording.Title}}
            </a>
            {{end}}
        </div>
    </div>

    <div class="main-content">
        <div class="player-container" id="player-container">
            {{if .Recordings}}
            <asciinema-player id="player" cols="120" rows="40" autoplay="false" speed="1.5"></asciinema-player>
            {{else}}
            <div class="empty-state">
                <h3>No recordings available</h3>
                <p>Run tests with -cinema flag to generate recordings.</p>
            </div>
            {{end}}
        </div>
    </div>

    <script>
        {{.JS}}
    </script>

    <script>
        const recordings = {
            {{range .Recordings}}
            "{{.Name}}": {
                title: "{{.Title}}",
                data: "{{.Content}}"
            },{{end}}
        };

        let currentPlayer = null;

        function loadRecording(filename) {
            const recording = recordings[filename];
            if (!recording) return;

            // Recording selected: recording.title

            // Decode recording data with proper UTF-8 handling
            const binaryString = atob(recording.data);
            const bytes = new Uint8Array(binaryString.length);
            for (let i = 0; i < binaryString.length; i++) {
                bytes[i] = binaryString.charCodeAt(i);
            }

            // Clear container and create player using API
            const container = document.getElementById('player-container');
            container.innerHTML = '<div id="player-target"></div>';

            // Create blob URL for the cast data with UTF-8 charset
            const blob = new Blob([bytes], { type: 'text/plain; charset=utf-8' });
            const url = URL.createObjectURL(blob);

            // Use AsciinemaPlayer.create API with blob URL
            try {
                AsciinemaPlayer.create(url, document.getElementById('player-target'), {
                    autoPlay: false,
                    loop: false
                });
                console.log('Player created successfully for:', recording.title);

                // Let the player size itself naturally - no scaling interference

            } catch (error) {
                console.error('Error creating player:', error, 'for', recording.title);
                // Fallback: create player element manually
                container.innerHTML = '<asciinema-player src="' + url + '" autoplay="false" loop="false"></asciinema-player>';
                console.log('Used fallback player element');
            }
        }

        // Handle recording selection
        document.querySelectorAll('.recording-item').forEach(item => {
            item.addEventListener('click', (e) => {
                e.preventDefault();

                // Update active state
                document.querySelectorAll('.recording-item').forEach(i => i.classList.remove('active'));
                item.classList.add('active');

                // Load recording
                const filename = item.dataset.recording;
                loadRecording(filename);
            });
        });

        // Wait for DOM to be ready
        if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', init);
        } else {
            init();
        }

        function init() {
            // Load first recording by default
            {{if .Recordings}}
            loadRecording("{{(index .Recordings 0).Name}}");
            {{end}}
        }
    </script>
</body>
</html>`

	t := template.Must(template.New("viewer").Parse(tmpl))

	var result strings.Builder
	data := struct {
		Recordings []Recording
		CSS        template.CSS
		JS         template.JS
	}{
		Recordings: recordings,
		CSS:        template.CSS(cssContent),
		JS:         template.JS(jsContent),
	}

	if err := t.Execute(&result, data); err != nil {
		log.Fatalf("Failed to execute template: %v", err)
	}

	return result.String()
}
