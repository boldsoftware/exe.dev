// Command terminal-test serves a standalone xterm terminal UI for testing.
// It serves the same HTML/JS/CSS as the production terminal but spawns a local
// bash shell instead of connecting to a VM via SSH.
//
// Usage:
//
//	go run ./cmd/terminal-test
//
// Then open http://localhost:8000/ in your browser.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// TerminalMessage represents a message sent from the client
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"` // For input messages
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// OutputMessage represents a message sent to the client
type OutputMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

func main() {
	port := "8000"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	// Serve static files from execore/static/
	staticDir := "execore/static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		log.Fatalf("Static directory not found: %s (run from /home/exedev/exe)", staticDir)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// Disable caching for development
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			http.ServeFile(w, r, staticDir+"/terminal.html")
			return
		}
		http.NotFound(w, r)
	})

	http.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		// Disable caching for development
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		filename := strings.TrimPrefix(r.URL.Path, "/static/")
		http.ServeFile(w, r, staticDir+"/"+filename)
	})

	http.HandleFunc("/terminal/ws/", handleTerminalWebSocket)

	log.Printf("Starting terminal-test server on http://localhost:%s/", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func handleTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		log.Printf("Failed to upgrade websocket: %v", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")

	ctx := r.Context()

	// Spawn bash with a PTY
	cmd := exec.Command("/bin/bash")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("Failed to start pty: %v", err)
		conn.Close(websocket.StatusInternalError, fmt.Sprintf("Failed to start pty: %v", err))
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Set initial size
	pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	var wg sync.WaitGroup

	// Read from PTY and send to WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				msg := OutputMessage{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(buf[:n]),
				}
				data, _ := json.Marshal(msg)
				err := conn.Write(ctx, websocket.MessageText, data)
				if err != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("PTY read error: %v", err)
				}
				return
			}
		}
	}()

	// Read from WebSocket and write to PTY
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}

		var msg TerminalMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("Failed to parse message: %v", err)
			continue
		}

		switch msg.Type {
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				log.Printf("Resize: %dx%d", msg.Cols, msg.Rows)
				pty.Setsize(ptmx, &pty.Winsize{Rows: msg.Rows, Cols: msg.Cols})
			}
		case "input":
			if msg.Data != "" {
				_, err := ptmx.Write([]byte(msg.Data))
				if err != nil {
					log.Printf("PTY write error: %v", err)
				}
			}
		}
	}

	conn.Close(websocket.StatusNormalClosure, "")
	wg.Wait()
}
