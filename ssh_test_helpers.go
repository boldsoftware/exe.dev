package exe

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// mustRead reads from the reader until the pattern is found or timeout occurs.
// It properly handles blocking reads by using a goroutine and channel.
// On timeout or error, it calls t.Fatal with a descriptive message.
func mustRead(t *testing.T, r io.Reader, pattern string, timeout time.Duration, context string) string {
	t.Helper()

	output := &bytes.Buffer{}
	found := make(chan bool, 1)
	errChan := make(chan error, 1)

	// Start a goroutine to do the blocking read
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), pattern) {
					found <- true
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
		}
	}()

	// Wait for pattern, error, or timeout
	select {
	case <-found:
		return output.String()
	case err := <-errChan:
		t.Fatalf("%s: Read error while waiting for pattern %q: %v\nOutput so far:\n%s",
			context, pattern, err, output.String())
	case <-time.After(timeout):
		t.Fatalf("%s: Timeout after %v waiting for pattern %q\nOutput received:\n%s",
			context, timeout, pattern, output.String())
	}

	// This should never be reached
	return output.String()
}

// mustWrite writes data to the writer and fails the test on error.
func mustWrite(t *testing.T, w io.Writer, data, context string) {
	t.Helper()

	n, err := w.Write([]byte(data))
	if err != nil {
		t.Fatalf("%s: Failed to write %q: %v", context, data, err)
	}
	if n != len(data) {
		t.Fatalf("%s: Partial write of %q: wrote %d bytes, expected %d",
			context, data, n, len(data))
	}
}

// mustWriteChars writes a string character by character with a small delay between each.
// This simulates typing and is useful for terminal input testing.
func mustWriteChars(t *testing.T, w io.Writer, text string, delay time.Duration, context string) {
	t.Helper()

	for i, ch := range text {
		n, err := w.Write([]byte{byte(ch)})
		if err != nil {
			t.Fatalf("%s: Failed to write character %c at position %d: %v",
				context, ch, i, err)
		}
		if n != 1 {
			t.Fatalf("%s: Failed to write character %c: wrote %d bytes, expected 1",
				context, ch, n)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

// readWithTimeout attempts to read from the reader with a timeout.
// Unlike mustRead, this doesn't fail the test - it returns what it could read and an error.
func readWithTimeout(r io.Reader, timeout time.Duration) (string, error) {
	output := &bytes.Buffer{}
	done := make(chan error, 1)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case err := <-done:
		if err == io.EOF {
			return output.String(), nil
		}
		return output.String(), err
	case <-time.After(timeout):
		return output.String(), fmt.Errorf("timeout after %v", timeout)
	}
}
