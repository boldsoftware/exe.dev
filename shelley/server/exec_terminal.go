package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/creack/pty"
)

// ExecMessage is the message format for terminal websocket communication
type ExecMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// handleExecWS handles websocket connections for executing shell commands
// Query params:
//   - cmd: the command to execute (required)
//   - cwd: working directory (optional, defaults to current dir)
func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	cmd := r.URL.Query().Get("cmd")
	if cmd == "" {
		http.Error(w, "cmd parameter required", http.StatusBadRequest)
		return
	}

	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			cwd = "/"
		}
	}

	// Upgrade to websocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.logger.Error("Failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")

	// Wait for init message with terminal size
	var initMsg ExecMessage
	if err := wsjson.Read(ctx, conn, &initMsg); err != nil {
		s.logger.Error("Failed to read init message", "error", err)
		conn.Close(websocket.StatusPolicyViolation, "no init message")
		return
	}

	if initMsg.Type != "init" {
		conn.Close(websocket.StatusPolicyViolation, "expected init message")
		return
	}

	cols := initMsg.Cols
	rows := initMsg.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	// Create command
	shellCmd := exec.CommandContext(ctx, "bash", "--login", "-c", cmd)
	shellCmd.Dir = cwd
	shellCmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Start with pty
	ptmx, err := pty.StartWithSize(shellCmd, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		s.logger.Error("Failed to start command with pty", "error", err, "cmd", cmd)
		errMsg := ExecMessage{
			Type: "error",
			Data: err.Error(),
		}
		wsjson.Write(ctx, conn, errMsg)
		conn.Close(websocket.StatusInternalError, "failed to start command")
		return
	}
	defer ptmx.Close()

	// Channel to signal when process exits with all output sent
	done := make(chan error, 1)

	// Read from pty and send to websocket, then wait for process and signal done
	go func() {
		// First, read all output from pty
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				msg := ExecMessage{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(buf[:n]),
				}
				if wsjson.Write(ctx, conn, msg) != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.logger.Debug("PTY read error", "error", err)
				}
				break
			}
		}
		// After pty is closed/EOF, wait for process exit status
		err := shellCmd.Wait()
		done <- err
	}()

	// Create a channel for incoming websocket messages
	msgChan := make(chan ExecMessage)
	errChan := make(chan error)

	// Goroutine to read websocket messages
	go func() {
		for {
			var msg ExecMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				errChan <- err
				return
			}
			msgChan <- msg
		}
	}()

	// Process messages and handle exit
	for {
		select {
		case <-ctx.Done():
			if shellCmd.Process != nil {
				shellCmd.Process.Kill()
			}
			return
		case exitErr := <-done:
			// Process exited
			exitCode := 0
			if exitErr != nil {
				if exitError, ok := exitErr.(*exec.ExitError); ok {
					exitCode = exitError.ExitCode()
				}
			}
			exitMsg := ExecMessage{
				Type: "exit",
				Data: fmt.Sprintf("%d", exitCode),
			}
			s.logger.Info("Sending exit message", "exitCode", exitCode)
			if err := wsjson.Write(ctx, conn, exitMsg); err != nil {
				s.logger.Error("Failed to write exit message", "error", err)
			}
			s.logger.Info("Closing websocket normally")
			conn.Close(websocket.StatusNormalClosure, "process exited")
			return
		case err := <-errChan:
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				s.logger.Debug("Websocket read error", "error", err)
			}
			if shellCmd.Process != nil {
				shellCmd.Process.Kill()
			}
			return
		case msg := <-msgChan:
			switch msg.Type {
			case "input":
				if msg.Data != "" {
					ptmx.Write([]byte(msg.Data))
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					setWinsize(ptmx, msg.Cols, msg.Rows)
				}
			}
		}
	}
}

// setWinsize sets the terminal window size
func setWinsize(f *os.File, cols, rows uint16) error {
	ws := struct {
		Rows uint16
		Cols uint16
		X    uint16
		Y    uint16
	}{
		Rows: rows,
		Cols: cols,
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}
