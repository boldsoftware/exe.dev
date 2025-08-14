package sshbuf

import (
	"context"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

const defaultBufferSize = 4096

type Channel struct {
	ch     ssh.Channel
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error
	done   chan struct{}
}

func New(ch ssh.Channel) *Channel {
	bc := &Channel{
		ch:   ch,
		buf:  make([]byte, 0, defaultBufferSize),
		done: make(chan struct{}),
	}
	bc.cond = sync.NewCond(&bc.mu)
	
	go bc.readLoop()
	
	return bc
}

func (bc *Channel) readLoop() {
	defer close(bc.done)
	
	readBuf := make([]byte, defaultBufferSize)
	for {
		n, err := bc.ch.Read(readBuf)
		
		bc.mu.Lock()
		if n > 0 {
			bc.buf = append(bc.buf, readBuf[:n]...)
			bc.cond.Signal()
		}
		if err != nil {
			bc.err = err
			bc.closed = true
			bc.cond.Broadcast()
			bc.mu.Unlock()
			return
		}
		bc.mu.Unlock()
	}
}

func (bc *Channel) Read(p []byte) (int, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	
	for len(bc.buf) == 0 && !bc.closed {
		bc.cond.Wait()
	}
	
	if len(bc.buf) == 0 && bc.closed {
		if bc.err != nil {
			return 0, bc.err
		}
		return 0, io.EOF
	}
	
	n := copy(p, bc.buf)
	bc.buf = bc.buf[n:]
	
	if len(bc.buf) == 0 && cap(bc.buf) > defaultBufferSize*2 {
		bc.buf = make([]byte, 0, defaultBufferSize)
	}
	
	return n, nil
}

func (bc *Channel) ReadCtx(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	
	bc.mu.Lock()
	defer bc.mu.Unlock()
	
	// Fast path: data already available or channel closed
	if len(bc.buf) > 0 || bc.closed {
		if len(bc.buf) == 0 && bc.closed {
			if bc.err != nil {
				return 0, bc.err
			}
			return 0, io.EOF
		}
		
		n := copy(p, bc.buf)
		bc.buf = bc.buf[n:]
		
		if len(bc.buf) == 0 && cap(bc.buf) > defaultBufferSize*2 {
			bc.buf = make([]byte, 0, defaultBufferSize)
		}
		
		return n, nil
	}
	
	// Slow path: need to wait for data with context cancellation
	done := make(chan struct{})
	var n int
	var err error
	
	go func() {
		bc.mu.Lock()
		defer bc.mu.Unlock()
		defer close(done)
		
		for len(bc.buf) == 0 && !bc.closed {
			bc.cond.Wait()
		}
		
		if len(bc.buf) == 0 && bc.closed {
			if bc.err != nil {
				err = bc.err
			} else {
				err = io.EOF
			}
			return
		}
		
		n = copy(p, bc.buf)
		bc.buf = bc.buf[n:]
		
		if len(bc.buf) == 0 && cap(bc.buf) > defaultBufferSize*2 {
			bc.buf = make([]byte, 0, defaultBufferSize)
		}
	}()
	
	// Release the lock while waiting
	bc.mu.Unlock()
	
	select {
	case <-ctx.Done():
		bc.mu.Lock()
		bc.cond.Broadcast()
		// Re-acquire the lock that defer will unlock
		return 0, ctx.Err()
	case <-done:
		// Re-acquire the lock that defer will unlock
		bc.mu.Lock()
		return n, err
	}
}

// Unread puts data back at the front of the buffer to be read again
// This is useful when you've read data that you need to "put back"
func (bc *Channel) Unread(data []byte) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	
	if len(data) == 0 {
		return
	}
	
	// Prepend the data to the buffer
	newBuf := make([]byte, len(data)+len(bc.buf))
	copy(newBuf, data)
	copy(newBuf[len(data):], bc.buf)
	bc.buf = newBuf
	
	// Signal any waiting readers
	bc.cond.Signal()
}

func (bc *Channel) Write(data []byte) (int, error) {
	return bc.ch.Write(data)
}

func (bc *Channel) Close() error {
	return bc.ch.Close()
}

func (bc *Channel) CloseWrite() error {
	return bc.ch.CloseWrite()
}

func (bc *Channel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return bc.ch.SendRequest(name, wantReply, payload)
}

func (bc *Channel) Stderr() io.ReadWriter {
	return bc.ch.Stderr()
}

var _ ssh.Channel = (*Channel)(nil)