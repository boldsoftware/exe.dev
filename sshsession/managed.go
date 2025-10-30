package sshsession

import (
	"context"

	"exe.dev/ctxio"
	gliderssh "github.com/gliderlabs/ssh"
)

// Session represents the managed session capabilities required by the SSH server.
type Session interface {
	gliderssh.Session
	ReadContext(context.Context, []byte) (int, error)
	ReadByteContext(context.Context) (byte, error)
	Push([]byte)
	CtxReader() *ctxio.Reader
}

// Managed wraps an SSH session to provide coordinated input handling.
// It centralizes reads from the underlying channel so callers can perform
// cancellable reads, push data back into the stream, and create specialized
// readers without fighting the crypto/ssh APIs.
type Managed struct {
	gliderssh.Session
	reader *ctxio.Reader
}

// NewManaged creates a Managed session wrapper around a gliderlabs session.
func NewManaged(base gliderssh.Session) Session {
	m := &Managed{
		Session: base,
		reader:  ctxio.NewReader(base),
	}
	return m
}

// Read implements io.Reader with buffered input coordination.
func (m *Managed) Read(p []byte) (int, error) {
	return m.reader.Read(p)
}

// ReadContext allows callers to perform a cancellable read.
func (m *Managed) ReadContext(ctx context.Context, p []byte) (int, error) {
	return m.reader.ReadContext(ctx, p)
}

func (m *Managed) ReadByteContext(ctx context.Context) (byte, error) {
	return m.reader.ReadByteContext(ctx)
}

// Push re-inserts data at the front of the input stream.
func (m *Managed) Push(data []byte) {
	m.reader.Insert(data)
}

func (m *Managed) CtxReader() *ctxio.Reader {
	return m.reader
}

// Shell wraps a session to adapt it to the command system requirements.
type Shell struct {
	Session
}

// NewShell creates a shell-oriented view of the managed session.
func NewShell(base Session) *Shell {
	return &Shell{Session: base}
}

// Context exposes the underlying session context using the standard interface.
func (s *Shell) Context() context.Context {
	return s.Session.Context()
}
