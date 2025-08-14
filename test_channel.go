package exe

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

// TestChannel provides a deterministic channel for testing without sleeps
type TestChannel struct {
	inputQueue  chan byte
	inputClosed bool
	inputMu     sync.Mutex
	outputBuf   *bytes.Buffer
	outputMu    sync.Mutex
	closed      chan struct{}
}

// NewTestChannel creates a new test channel
func NewTestChannel() *TestChannel {
	return &TestChannel{
		inputQueue: make(chan byte, 1024),
		outputBuf:  &bytes.Buffer{},
		closed:     make(chan struct{}),
	}
}

// Write implements io.Writer
func (tc *TestChannel) Write(data []byte) (int, error) {
	tc.outputMu.Lock()
	defer tc.outputMu.Unlock()

	select {
	case <-tc.closed:
		return 0, io.ErrClosedPipe
	default:
	}

	return tc.outputBuf.Write(data)
}

// Read implements io.Reader
func (tc *TestChannel) Read(p []byte) (int, error) {
	select {
	case <-tc.closed:
		return 0, io.EOF
	default:
	}

	n := 0
	for n < len(p) {
		select {
		case b, ok := <-tc.inputQueue:
			if !ok {
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			p[n] = b
			n++
		default:
			if n > 0 {
				return n, nil
			}
			// Block waiting for at least one byte
			select {
			case b, ok := <-tc.inputQueue:
				if !ok {
					return 0, io.EOF
				}
				p[n] = b
				n++
			case <-tc.closed:
				return 0, io.EOF
			}
		}
	}
	return n, nil
}

// ReadCtx implements context-aware reading
func (tc *TestChannel) ReadCtx(ctx context.Context, p []byte) (int, error) {
	select {
	case <-tc.closed:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	n := 0
	for n < len(p) {
		select {
		case b, ok := <-tc.inputQueue:
			if !ok {
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			p[n] = b
			n++
		case <-ctx.Done():
			if n > 0 {
				return n, nil
			}
			return 0, ctx.Err()
		case <-tc.closed:
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		default:
			if n > 0 {
				return n, nil
			}
			// Block waiting for at least one byte
			select {
			case b, ok := <-tc.inputQueue:
				if !ok {
					return 0, io.EOF
				}
				p[n] = b
				n++
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-tc.closed:
				return 0, io.EOF
			}
		}
	}
	return n, nil
}

// SendInput sends data to be read
func (tc *TestChannel) SendInput(data string) {
	tc.inputMu.Lock()
	defer tc.inputMu.Unlock()

	if tc.inputClosed {
		return
	}

	for _, b := range []byte(data) {
		select {
		case tc.inputQueue <- b:
			// Successfully sent byte
		case <-tc.closed:
			return
		}
	}
}

// SendInputString is an alias for SendInput for compatibility
func (tc *TestChannel) SendInputString(data string) {
	tc.SendInput(data)
}

// GetOutput returns all written output
func (tc *TestChannel) GetOutput() string {
	tc.outputMu.Lock()
	defer tc.outputMu.Unlock()
	return tc.outputBuf.String()
}

// Close closes the channel
func (tc *TestChannel) Close() error {
	tc.inputMu.Lock()
	defer tc.inputMu.Unlock()

	if !tc.inputClosed {
		tc.inputClosed = true
		close(tc.inputQueue)
		close(tc.closed)
	}
	return nil
}

// CloseWrite closes write side
func (tc *TestChannel) CloseWrite() error {
	return nil
}

// SendRequest implements SSH channel interface
func (tc *TestChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

// Stderr returns stderr (same as stdout for testing)
func (tc *TestChannel) Stderr() io.ReadWriter {
	return tc
}

// TestChannelPair provides bidirectional test channels
type TestChannelPair struct {
	ServerSide *TestChannel
	ClientSide *TestChannel
}

// NewTestChannelPair creates a connected pair of test channels
func NewTestChannelPair() *TestChannelPair {
	return &TestChannelPair{
		ServerSide: NewTestChannel(),
		ClientSide: NewTestChannel(),
	}
}

// ScriptedChannel provides scripted responses for testing
type ScriptedChannel struct {
	*TestChannel
	script []struct {
		expect string
		send   string
	}
	scriptIndex int
	mu          sync.Mutex
}

// NewScriptedChannel creates a channel with scripted responses
func NewScriptedChannel() *ScriptedChannel {
	return &ScriptedChannel{
		TestChannel: NewTestChannel(),
		script:      []struct{ expect, send string }{},
	}
}

// AddScript adds an expected input and response
func (sc *ScriptedChannel) AddScript(expect, send string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.script = append(sc.script, struct{ expect, send string }{expect, send})
}

// RunScript executes the script
func (sc *ScriptedChannel) RunScript() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	for sc.scriptIndex < len(sc.script) {
		step := sc.script[sc.scriptIndex]

		// Wait for expected output
		deadline := time.After(2 * time.Second)
		for {
			output := sc.GetOutput()
			if len(output) >= len(step.expect) && output[len(output)-len(step.expect):] == step.expect {
				break
			}
			select {
			case <-deadline:
				return io.ErrUnexpectedEOF
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}

		// Send response
		if step.send != "" {
			sc.SendInput(step.send)
		}

		sc.scriptIndex++
	}

	return nil
}
