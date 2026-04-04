package execore

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"golang.org/x/term"
)

type mockReadWriter struct {
	toSend       []byte
	bytesPerRead int
	received     []byte
}

func (m *mockReadWriter) Read(data []byte) (int, error) {
	n := len(data)
	if n == 0 {
		return 0, nil
	}
	if n > len(m.toSend) {
		n = len(m.toSend)
	}
	if n == 0 {
		return 0, io.EOF
	}
	if m.bytesPerRead > 0 && n > m.bytesPerRead {
		n = m.bytesPerRead
	}
	copy(data, m.toSend[:n])
	m.toSend = m.toSend[n:]
	return n, nil
}

func (m *mockReadWriter) Write(data []byte) (int, error) {
	m.received = append(m.received, data...)
	return len(data), nil
}

func TestEndsWithContinuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want bool
	}{
		{"empty", "", false},
		{"no backslash", "hello", false},
		{"trailing backslash", `hello \`, true},
		{"trailing backslash no space", `hello\`, true},
		{"escaped backslash", `hello\\`, false},
		{"escaped backslash then continuation", `hello\\\`, true},
		{"four backslashes", `hello\\\\`, false},
		{"just backslash", `\`, true},
		{"just two backslashes", `\\`, false},
		{"trailing spaces after backslash", "hello \\  ", true},
		{"trailing tab after backslash", "hello \\\t", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := endsWithContinuation(tt.line)
			if got != tt.want {
				t.Errorf("endsWithContinuation(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestJoinContinuationLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		firstLine string
		// remaining terminal input (use \r for line endings)
		input string
		want  string
	}{
		{
			name:      "no continuation",
			firstLine: "hello",
			want:      "hello",
		},
		{
			name:      "single continuation",
			firstLine: `ls \`,
			input:     "--json\r",
			want:      "ls --json",
		},
		{
			name:      "multiple continuations",
			firstLine: `cmd \`,
			input:     "--flag1 val1 \\\r--flag2 val2\r",
			want:      "cmd --flag1 val1 --flag2 val2",
		},
		{
			name:      "continuation no trailing space",
			firstLine: `hel\`,
			input:     "lo\r",
			want:      "hello",
		},
		{
			name:      "continuation with trailing whitespace",
			firstLine: "cmd \\  ",
			input:     "arg\r",
			want:      "cmd arg",
		},
		{
			name:      "escaped backslash not continuation",
			firstLine: `hello\\`,
			want:      `hello\\`,
		},
		{
			name:      "empty continuation line",
			firstLine: `cmd \`,
			input:     "\r",
			want:      "cmd ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockReadWriter{
				toSend:       []byte(tt.input),
				bytesPerRead: 1,
			}
			terminal := term.NewTerminal(mock, "> ")
			got, err := joinContinuationLines(tt.firstLine, terminal)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJoinContinuationLinesCancelled(t *testing.T) {
	t.Parallel()
	mock := &mockReadWriter{
		toSend: []byte{}, // EOF immediately
	}
	terminal := term.NewTerminal(mock, "> ")
	_, err := joinContinuationLines(`cmd \`, terminal)
	if err != errContinuationCancelled {
		t.Errorf("expected errContinuationCancelled, got %v", err)
	}
	// Verify visual feedback was written through the terminal.
	if !bytes.Contains(mock.received, []byte("\r\n")) {
		t.Errorf("expected \\r\\n in terminal output, got %q", mock.received)
	}
}

type errReadWriter struct {
	err      error
	received []byte
}

func (m *errReadWriter) Read([]byte) (int, error) {
	return 0, m.err
}

func (m *errReadWriter) Write(data []byte) (int, error) {
	m.received = append(m.received, data...)
	return len(data), nil
}

func TestJoinContinuationLinesTransportError(t *testing.T) {
	t.Parallel()
	transportErr := errors.New("connection reset")
	mock := &errReadWriter{err: transportErr}
	terminal := term.NewTerminal(mock, "> ")
	_, err := joinContinuationLines(`cmd \`, terminal)
	if err != transportErr {
		t.Errorf("expected transport error, got %v", err)
	}
}

func TestJoinContinuationLinesHistory(t *testing.T) {
	t.Parallel()
	mock := &mockReadWriter{
		toSend:       []byte("--flag1 val1 \\\r--flag2 val2\r"),
		bytesPerRead: 1,
	}
	terminal := term.NewTerminal(mock, "> ")
	// Simulate what ReadLine does: add the first line to history.
	terminal.History.Add(`cmd \`)
	got, err := joinContinuationLines(`cmd \`, terminal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "cmd --flag1 val1 --flag2 val2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// History should contain only the joined result, not the fragments.
	if n := terminal.History.Len(); n != 1 {
		t.Fatalf("history length = %d, want 1", n)
	}
	if h := terminal.History.At(0); h != want {
		t.Errorf("history[0] = %q, want %q", h, want)
	}
}
