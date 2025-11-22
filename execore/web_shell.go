// Support for /shell on the web looking like SSH'ing into the exe.dev console
//
// TODO(philip): Could this be leaking any resources?

package execore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gliderlabs/ssh"

	"exe.dev/exedb"
)

// WebShellSession represents a web-based shell session that implements exemenu.ShellSession
type WebShellSession struct {
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	ptyReq     *ssh.Pty
	ptyReady   chan struct{}
	winChMutex sync.Mutex
	winCh      chan ssh.Window
	user       *exedb.User
	publicKey  string
	readBuf    []byte
	readMutex  sync.Mutex
	readCond   *sync.Cond
	writeMutex sync.Mutex
}

// WebShellMessage represents messages sent over the websocket
type WebShellMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// handleWebShell serves the web shell page at /shell
func (s *Server) handleWebShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		// Not authenticated - redirect to login
		// TODO(philip): We could do user registration here too if we wanted to.
		scheme := getScheme(r)
		returnURL := fmt.Sprintf("%s://%s/shell", scheme, r.Host)
		authURL := fmt.Sprintf("%s://%s/auth?redirect=%s", scheme, r.Host, returnURL)
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	// Get user info
	user, err := withRxRes(s, r.Context(), func(ctx context.Context, q *exedb.Queries) (exedb.User, error) {
		return q.GetUserWithDetails(ctx, userID)
	})
	if err != nil {
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	// Serve the shell HTML page
	data := UserPageData{
		Env:        s.env,
		User:       user,
		ActivePage: "shell",
		IsLoggedIn: true,
	}
	s.renderTemplate(w, "shell.html", data)
}

// handleWebShellWS handles websocket connections for the web shell
func (s *Server) handleWebShellWS(w http.ResponseWriter, r *http.Request) {
	// Check authentication
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Get user info
	user, err := withRxRes(s, r.Context(), func(ctx context.Context, q *exedb.Queries) (exedb.User, error) {
		return q.GetUserWithDetails(ctx, userID)
	})
	if err != nil {
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	// Upgrade to websocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")

	// Create session context
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Create web shell session
	userCopy := user
	session := &WebShellSession{
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		user:      &userCopy,
		publicKey: "", // Web sessions don't have SSH keys
		ptyReady:  make(chan struct{}),
		winCh:     make(chan ssh.Window, 1),
		readBuf:   make([]byte, 0),
	}
	session.readCond = sync.NewCond(&session.readMutex)

	// Start message reader goroutine
	go session.readMessages()

	// Wait for initial PTY request
	select {
	case <-session.ptyReady:
		// PTY request received, continue
	case <-time.After(10 * time.Second):
		conn.Close(websocket.StatusPolicyViolation, "no PTY request received")
		return
	case <-ctx.Done():
		return
	}

	// Run the shell
	s.sshServer.runMainShellWithReadline(session, session.publicKey, session.user)

	// Clean shutdown
	conn.Close(websocket.StatusNormalClosure, "")
}

// readMessages reads messages from the websocket
func (ws *WebShellSession) readMessages() {
	for {
		select {
		case <-ws.ctx.Done():
			return
		default:
		}

		var msg WebShellMessage
		err := wsjson.Read(ws.ctx, ws.conn, &msg)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				slog.Debug("Websocket read error", "error", err)
			}
			ws.cancel()
			return
		}

		switch msg.Type {
		case "init":
			// Initial PTY request
			ws.ptyReq = &ssh.Pty{
				Term:   "xterm-256color",
				Window: ssh.Window{Width: int(msg.Cols), Height: int(msg.Rows)},
			}
			// Signal that PTY is ready
			close(ws.ptyReady)
		case "resize":
			// Window resize
			ws.winChMutex.Lock()
			select {
			case ws.winCh <- ssh.Window{Width: int(msg.Cols), Height: int(msg.Rows)}:
			default:
				// Channel full, drop oldest
				select {
				case <-ws.winCh:
				default:
				}
				ws.winCh <- ssh.Window{Width: int(msg.Cols), Height: int(msg.Rows)}
			}
			ws.winChMutex.Unlock()
		case "input":
			// User input
			ws.readMutex.Lock()
			ws.readBuf = append(ws.readBuf, []byte(msg.Data)...)
			ws.readCond.Broadcast()
			ws.readMutex.Unlock()
		}
	}
}

// Implement ssh.Session interface

func (ws *WebShellSession) Read(p []byte) (n int, err error) {
	ws.readMutex.Lock()
	defer ws.readMutex.Unlock()

	for len(ws.readBuf) == 0 {
		select {
		case <-ws.ctx.Done():
			return 0, io.EOF
		default:
		}
		ws.readCond.Wait()
		select {
		case <-ws.ctx.Done():
			return 0, io.EOF
		default:
		}
	}

	n = copy(p, ws.readBuf)
	ws.readBuf = ws.readBuf[n:]
	return n, nil
}

func (ws *WebShellSession) ReadByteContext(ctx context.Context) (byte, error) {
	var buf [1]byte
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			default:
			}
		}
		n, err := ws.Read(buf[:])
		if n == 1 {
			return buf[0], err
		}
		if err != nil {
			return 0, err
		}
	}
}

func (ws *WebShellSession) Write(p []byte) (n int, err error) {
	ws.writeMutex.Lock()
	defer ws.writeMutex.Unlock()

	select {
	case <-ws.ctx.Done():
		return 0, io.EOF
	default:
	}

	msg := WebShellMessage{
		Type: "output",
		Data: string(p),
	}

	err = wsjson.Write(ws.ctx, ws.conn, msg)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Context returns the session context
func (ws *WebShellSession) Context() context.Context {
	return ws.ctx
}

// Environ returns environment variables (empty for web sessions)
func (ws *WebShellSession) Environ() []string {
	return []string{}
}

// User returns the username (empty for web sessions)
func (ws *WebShellSession) User() string {
	return ""
}

// Pty returns PTY information and window size change channel
func (ws *WebShellSession) Pty() (ssh.Pty, <-chan ssh.Window, bool) {
	if ws.ptyReq == nil {
		return ssh.Pty{}, nil, false
	}
	return *ws.ptyReq, ws.winCh, true
}

func (ws *WebShellSession) Push(data []byte) {
	if len(data) == 0 {
		return
	}
	ws.readMutex.Lock()
	ws.readBuf = append(append([]byte{}, data...), ws.readBuf...)
	ws.readCond.Broadcast()
	ws.readMutex.Unlock()
}
