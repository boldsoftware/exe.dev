package execore

import (
	"bytes"
	"context"
	"io"

	"exe.dev/exemenu"
	tea "github.com/charmbracelet/bubbletea"
)

// gameSessionInput adapts an SSH session for use as Bubble Tea input.
// It reads full buffers from the session so that multi-byte escape sequences
// (e.g. arrow keys: ESC [ A) arrive intact for Bubble Tea to parse.
// Ctrl+C (byte 3) is treated as EOF to quit.
type gameSessionInput struct {
	session io.Reader
}

func (t *gameSessionInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := t.session.Read(p)
	if n > 0 {
		// Scan for Ctrl+C in the read data.
		if i := bytes.IndexByte(p[:n], 3); i >= 0 {
			// Return everything before Ctrl+C, then EOF on next read.
			return i, io.EOF
		}
	}
	return n, err
}

func (ss *SSHServer) handleGameCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	width, height := cc.PtySize()
	model := newGameModel(width, height)

	input := &gameSessionInput{session: cc.SSHSession}

	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(cc.SSHSession),
	)

	if _, err := program.Run(); err != nil {
		return err
	}

	cc.Write("\n")
	return nil
}
