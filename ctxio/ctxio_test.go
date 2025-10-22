package ctxio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestCancelReaderReadContextDeliversUnderlyingData(t *testing.T) {
	cr := NewReader(bytes.NewBufferString("hello world"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	buf := make([]byte, 5)
	n, err := cr.ReadContext(ctx, buf)
	if err != nil {
		t.Fatalf("ReadContext error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Fatalf("ReadContext returned %q, want %q", got, "hello")
	}

	buf = make([]byte, 6)
	n, err = cr.ReadContext(ctx, buf)
	if err != nil {
		t.Fatalf("ReadContext error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != " world" {
		t.Fatalf("ReadContext returned %q, want %q", got, " world")
	}

	n, err = cr.ReadContext(ctx, buf[:0])
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final ReadContext error = %v, want io.EOF", err)
	}
	if n != 0 {
		t.Fatalf("final ReadContext returned %d bytes, want 0", n)
	}
}

func TestCancelReaderInsertPrecedesUnderlyingData(t *testing.T) {
	cr := NewReader(bytes.NewBufferString("world"))
	insert := []byte("hello ")
	cr.Insert(insert)
	copy(insert, "garbage") // inserting must clone, so corruption should not leak back

	buf := make([]byte, len("hello "))
	n, err := cr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "hello " {
		t.Fatalf("Read() returned %q, want %q", got, "hello ")
	}

	buf = make([]byte, len("world"))
	n, err = cr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "world" {
		t.Fatalf("Read() returned %q, want %q", got, "world")
	}

	n, err = cr.Read(buf[:0])
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final Read() error = %v, want io.EOF", err)
	}
	if n != 0 {
		t.Fatalf("final Read() returned %d bytes, want 0", n)
	}
}

func TestCancelReaderInsertIsLIFO(t *testing.T) {
	cr := NewReader(bytes.NewBufferString("base"))
	cr.Insert([]byte("first "))
	cr.Insert([]byte("second "))

	buf := make([]byte, len("second "))
	n, err := cr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "second " {
		t.Fatalf("Read() returned %q, want %q", got, "second ")
	}

	buf = make([]byte, len("first "))
	n, err = cr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "first " {
		t.Fatalf("Read() returned %q, want %q", got, "first ")
	}

	buf = make([]byte, len("base"))
	n, err = cr.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if got := string(buf[:n]); got != "base" {
		t.Fatalf("Read() returned %q, want %q", got, "base")
	}

	n, err = cr.Read(buf[:0])
	if !errors.Is(err, io.EOF) {
		t.Fatalf("final Read() error = %v, want io.EOF", err)
	}
	if n != 0 {
		t.Fatalf("final Read() returned %d bytes, want 0", n)
	}
}

func TestCancelReaderReadContextWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cr := NewReader(bytes.NewBufferString("ignored"))
	buf := make([]byte, 4)

	n, err := cr.ReadContext(ctx, buf)
	if n != 0 {
		t.Fatalf("ReadContext returned %d bytes, want 0", n)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadContext error = %v, want context.Canceled", err)
	}
}

func TestCancelReaderReadByteContextWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cr := NewReader(bytes.NewBufferString("ignored"))

	b, err := cr.ReadByteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadByteContext error = %v, want context.Canceled", err)
	}
	if b != 0 {
		t.Fatalf("ReadByteContext returned byte %d, want 0 when no data is read", b)
	}
}

func TestCancelReaderReadByteContextSuccess(t *testing.T) {
	cr := NewReader(bytes.NewBufferString("ok"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	b, err := cr.ReadByteContext(ctx)
	if err != nil {
		t.Fatalf("ReadByteContext error for first byte = %v, want nil", err)
	}
	if b != 'o' {
		t.Fatalf("ReadByteContext returned byte %q, want %q", b, 'o')
	}

	b, err = cr.ReadByteContext(ctx)
	if err != nil {
		t.Fatalf("ReadByteContext error for second byte = %v, want nil", err)
	}
	if b != 'k' {
		t.Fatalf("ReadByteContext returned byte %q on second call, want %q", b, 'k')
	}

	b, err = cr.ReadByteContext(ctx)
	if b != 0 {
		t.Fatalf("ReadByteContext returned byte %d after EOF, want 0", b)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("ReadByteContext error after EOF = %v, want io.EOF", err)
	}
}

func TestCancelReaderPropagatesUnderlyingErrors(t *testing.T) {
	rr := &recordingReader{
		errs: []error{errors.New("boom")},
	}
	cr := NewReader(rr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	buf := make([]byte, 1)
	_, err := cr.ReadContext(ctx, buf)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("ReadContext error = %v, want boom", err)
	}
}

func TestCancelReaderContextCancelWhileBackgroundReadPending(t *testing.T) {
	br := newBlockingReader()
	cr := NewReader(br)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := cr.ReadContext(ctx, buf)
		errCh <- err
	}()

	br.WaitUntilStarted()
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadContext error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadContext did not return after context cancel")
	}

	br.Release([]byte{42}, io.EOF)
}

func TestCancelReaderPendingBufferedAcrossReads(t *testing.T) {
	data := []byte("abcdef")
	rr := &recordingReader{
		chunks: [][]byte{append([]byte(nil), data...)},
		errs:   []error{io.EOF},
	}
	cr := NewReader(rr)

	first := make([]byte, 2)
	n, err := cr.Read(first)
	if err != nil {
		t.Fatalf("Read error = %v, want nil", err)
	}
	if got := string(first[:n]); got != "ab" {
		t.Fatalf("first read returned %q, want %q", got, "ab")
	}

	second := make([]byte, 2)
	n, err = cr.Read(second)
	if err != nil {
		t.Fatalf("Read error = %v, want nil", err)
	}
	if got := string(second[:n]); got != "cd" {
		t.Fatalf("second read returned %q, want %q", got, "cd")
	}

	third := make([]byte, 2)
	n, err = cr.Read(third)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Read error = %v, want io.EOF", err)
	}
	if got := string(third[:n]); got != "ef" {
		t.Fatalf("third read returned %q, want %q", got, "ef")
	}

	if calls := rr.Calls(); calls != 1 {
		t.Fatalf("underlying reader invoked %d times, want 1", calls)
	}
}

type recordingReader struct {
	mu     sync.Mutex
	chunks [][]byte
	errs   []error
	idx    int
	calls  int
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	rr.mu.Lock()
	defer rr.mu.Unlock()

	rr.calls++
	if rr.idx >= len(rr.chunks) {
		if rr.idx < len(rr.errs) {
			return 0, rr.errs[rr.idx]
		}
		return 0, io.EOF
	}

	n := copy(p, rr.chunks[rr.idx])
	rr.chunks[rr.idx] = rr.chunks[rr.idx][n:]
	if len(rr.chunks[rr.idx]) == 0 {
		if rr.idx < len(rr.errs) {
			err := rr.errs[rr.idx]
			rr.idx++
			return n, err
		}
		rr.idx++
		return n, nil
	}
	return n, nil
}

func (rr *recordingReader) Calls() int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.calls
}

type blockingReader struct {
	started chan struct{}
	release chan readResult
	once    sync.Once
}

type readResult struct {
	data []byte
	err  error
}

func newBlockingReader() *blockingReader {
	return &blockingReader{
		started: make(chan struct{}),
		release: make(chan readResult, 1),
	}
}

func (br *blockingReader) Read(p []byte) (int, error) {
	br.once.Do(func() { close(br.started) })
	res := <-br.release
	n := copy(p, res.data)
	return n, res.err
}

func (br *blockingReader) WaitUntilStarted() {
	<-br.started
}

func (br *blockingReader) Release(data []byte, err error) {
	br.release <- readResult{
		data: data,
		err:  err,
	}
}
