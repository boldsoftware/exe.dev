package ctxio

import (
	"bytes"
	"context"
	"io"
	"sync"
)

// NewReader wraps r with a Reader that supports context-aware reads and byte insertion.
// The provided reader must not be nil.
func NewReader(r io.Reader) *Reader {
	if r == nil {
		panic("ctxio.NewReader: reader is nil")
	}
	return &Reader{r: r}
}

type bufReader struct {
	buf  []byte
	err  error
	done bool
}

func (br *bufReader) Read(p []byte) (int, error) {
	n := copy(p, br.buf)
	br.buf = br.buf[n:]
	if len(br.buf) == 0 {
		br.done = true
		return n, br.err
	}
	return n, nil
}

// A Reader is an io.Reader that supports context-aware reads.
// It also supports inserting data back into the stream.
// No Reader methods are safe for concurrent use.
type Reader struct {
	r        io.Reader
	mu       sync.Mutex // protects following
	inFlight chan struct{}
	pending  []*bufReader
}

func (cr *Reader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	cr.mu.Lock()
	if len(cr.pending) > 0 {
		defer cr.mu.Unlock()
		return cr.readPendingLocked(p)
	}
	wait := cr.inFlight
	if wait == nil {
		wait = make(chan struct{})
		cr.inFlight = wait
		go cr.backgroundRead()
	}

	cr.mu.Unlock()
	select {
	case <-wait:
		cr.mu.Lock()
		defer cr.mu.Unlock()
		return cr.readPendingLocked(p)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (cr *Reader) backgroundRead() {
	tmp := make([]byte, 1024) // TODO: accept buffer size in constructor
	n, err := cr.r.Read(tmp)
	br := &bufReader{
		buf: tmp[:n],
		err: err,
	}
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.pending = append(cr.pending, br)
	close(cr.inFlight)
	cr.inFlight = nil
}

func (cr *Reader) readPendingLocked(p []byte) (int, error) {
	if len(cr.pending) == 0 {
		panic("ctxio.Reader: internal inconsistency; did you issue concurrent Reads?")
	}
	n, err := cr.pending[0].Read(p)
	if cr.pending[0].done {
		cr.pending = cr.pending[1:]
	}
	return n, err
}

// Read implements io.Reader.
func (cr *Reader) Read(p []byte) (int, error) {
	return cr.ReadContext(context.Background(), p)
}

// ReadByteContext reads a single byte with context cancellation support.
// Its semantics match io.ReadFull for a single-byte buffer: it returns the byte
// along with any error produced by the underlying reader once the byte has been read.
// When no byte can be read, it returns zero and the encountered error.
func (cr *Reader) ReadByteContext(ctx context.Context) (byte, error) {
	var buf [1]byte
	for {
		n, err := cr.ReadContext(ctx, buf[:])
		if n == 1 {
			return buf[0], err
		}
		if err != nil {
			return 0, err
		}
	}
}

func (cr *Reader) Insert(p []byte) {
	if len(p) == 0 {
		return
	}
	cr.mu.Lock()
	defer cr.mu.Unlock()
	br := &bufReader{buf: bytes.Clone(p)}
	cr.pending = append([]*bufReader{br}, cr.pending...)
}
