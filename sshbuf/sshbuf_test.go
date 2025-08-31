package sshbuf

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type mockChannel struct {
	mu          sync.Mutex
	readData    []byte
	readPos     int
	readErr     error
	writeData   []byte
	closed      bool
	closedWrite bool
	readDelay   time.Duration
}

func (m *mockChannel) Read(data []byte) (int, error) {
	if m.readDelay > 0 {
		time.Sleep(m.readDelay)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return 0, m.readErr
	}

	if m.readPos >= len(m.readData) {
		return 0, io.EOF
	}

	n := copy(data, m.readData[m.readPos:])
	m.readPos += n
	return n, nil
}

func (m *mockChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, errors.New("channel closed")
	}

	m.writeData = append(m.writeData, data...)
	return len(data), nil
}

func (m *mockChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	return nil
}

func (m *mockChannel) CloseWrite() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closedWrite = true
	return nil
}

func (m *mockChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return true, nil
}

func (m *mockChannel) Stderr() io.ReadWriter {
	return nil
}

var _ ssh.Channel = (*mockChannel)(nil)

func TestBasicRead(t *testing.T) {
	testData := []byte("Hello, World!")
	mock := &mockChannel{readData: testData}
	bc := New(mock)

	buf := make([]byte, 128)
	n, err := bc.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n != len(testData) {
		t.Fatalf("expected %d bytes, got %d", len(testData), n)
	}

	if string(buf[:n]) != string(testData) {
		t.Fatalf("expected %q, got %q", testData, buf[:n])
	}
}

func TestMultipleReads(t *testing.T) {
	testData := []byte("Hello, World! This is a longer message for testing.")
	mock := &mockChannel{readData: testData}
	bc := New(mock)

	buf := make([]byte, 10)
	var received []byte

	for {
		n, err := bc.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		received = append(received, buf[:n]...)
	}

	if string(received) != string(testData) {
		t.Fatalf("expected %q, got %q", testData, received)
	}
}

func TestReadCtxCancellation(t *testing.T) {
	mock := &mockChannel{
		readData:  []byte("data"),
		readDelay: 100 * time.Millisecond,
	}
	bc := New(mock)

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	buf := make([]byte, 128)
	_, err := bc.ReadCtx(ctx, buf)

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReadCtxImmediate(t *testing.T) {
	testData := []byte("immediate data")
	mock := &mockChannel{readData: testData}
	bc := New(mock)

	time.Sleep(50 * time.Millisecond)

	ctx := t.Context()
	buf := make([]byte, 128)
	n, err := bc.ReadCtx(ctx, buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n != len(testData) {
		t.Fatalf("expected %d bytes, got %d", len(testData), n)
	}

	if string(buf[:n]) != string(testData) {
		t.Fatalf("expected %q, got %q", testData, buf[:n])
	}
}

func TestReadCtxTimeout(t *testing.T) {
	mock := &mockChannel{
		readData:  []byte("data"),
		readDelay: 100 * time.Millisecond,
	}
	bc := New(mock)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	buf := make([]byte, 128)
	_, err := bc.ReadCtx(ctx, buf)

	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestConcurrentReads(t *testing.T) {
	testData := make([]byte, 1000)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	mock := &mockChannel{readData: testData}
	bc := New(mock)

	var wg sync.WaitGroup
	readers := 5
	wg.Add(readers)

	results := make([][]byte, readers)

	for i := 0; i < readers; i++ {
		go func(idx int) {
			defer wg.Done()

			buf := make([]byte, 100)
			for {
				n, err := bc.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("reader %d: unexpected error: %v", idx, err)
					return
				}
				results[idx] = append(results[idx], buf[:n]...)
			}
		}(i)
	}

	wg.Wait()

	var combined []byte
	for _, result := range results {
		combined = append(combined, result...)
	}

	if len(combined) != len(testData) {
		t.Fatalf("expected %d bytes total, got %d", len(testData), len(combined))
	}
}

func TestWrite(t *testing.T) {
	mock := &mockChannel{}
	bc := New(mock)

	testData := []byte("write test")
	n, err := bc.Write(testData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n != len(testData) {
		t.Fatalf("expected %d bytes written, got %d", len(testData), n)
	}

	if string(mock.writeData) != string(testData) {
		t.Fatalf("expected %q, got %q", testData, mock.writeData)
	}
}

func TestClose(t *testing.T) {
	mock := &mockChannel{}
	bc := New(mock)

	err := bc.Close()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.closed {
		t.Fatal("expected channel to be closed")
	}
}

func TestCloseWrite(t *testing.T) {
	mock := &mockChannel{}
	bc := New(mock)

	err := bc.CloseWrite()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.closedWrite {
		t.Fatal("expected channel write to be closed")
	}
}

func TestReadError(t *testing.T) {
	expectedErr := errors.New("read error")
	mock := &mockChannel{
		readErr: expectedErr,
	}
	bc := New(mock)

	buf := make([]byte, 128)
	_, err := bc.Read(buf)

	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestLargeBuffer(t *testing.T) {
	testData := make([]byte, defaultBufferSize*3)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	mock := &mockChannel{readData: testData}
	bc := New(mock)

	buf := make([]byte, defaultBufferSize*4)
	var received []byte

	for {
		n, err := bc.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		received = append(received, buf[:n]...)
	}

	if len(received) != len(testData) {
		t.Fatalf("expected %d bytes, got %d", len(testData), len(received))
	}

	for i := range testData {
		if received[i] != testData[i] {
			t.Fatalf("byte mismatch at position %d: expected %d, got %d", i, testData[i], received[i])
		}
	}
}
