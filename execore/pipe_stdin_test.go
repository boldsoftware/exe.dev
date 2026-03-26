package execore

import (
	"io"
	"testing"
	"testing/synctest"
)

func TestPipeStdinForwardsBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		underlying, underlyingW := io.Pipe()
		r, stop := pipeStdin(underlying, func([]byte) {
			t.Error("unexpected pushBack")
		})

		// Simulate a consumer (like x/crypto/ssh's internal io.Copy).
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, 64)
			n, _ := r.Read(buf)
			got <- string(buf[:n])
		}()

		go underlyingW.Write([]byte("hello"))
		synctest.Wait()

		if s := <-got; s != "hello" {
			t.Fatalf("got %q, want %q", s, "hello")
		}

		stop()
		underlyingW.Close() // let goroutine exit cleanly
	})
}

func TestPipeStdinPushesBackOnStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		underlying, underlyingW := io.Pipe()
		var pushed []byte
		r, stop := pipeStdin(underlying, func(data []byte) {
			pushed = append(pushed, data...)
		})

		// Start a consumer that drains the pipe until EOF.
		consumerDone := make(chan struct{})
		go func() {
			defer close(consumerDone)
			io.Copy(io.Discard, r)
		}()

		// Send "hello" through and let everything settle.
		go underlyingW.Write([]byte("hello"))
		synctest.Wait()

		// Stop the bridge (simulates SSH session ending).
		// Consumer sees EOF and exits.
		stop()
		synctest.Wait()
		<-consumerDone

		// Now write "x" to the underlying reader.
		// The bridge goroutine reads it, can't write to the closed pipe,
		// and pushes it back.
		go underlyingW.Write([]byte("x"))
		synctest.Wait()

		if string(pushed) != "x" {
			t.Fatalf("pushed = %q, want %q", pushed, "x")
		}

		underlyingW.Close()
	})
}

func TestPipeStdinHandlesReadError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		underlying, underlyingW := io.Pipe()
		r, stop := pipeStdin(underlying, func([]byte) {
			t.Error("unexpected pushBack")
		})
		defer stop()

		// Close the underlying writer with an error.
		underlyingW.CloseWithError(io.ErrUnexpectedEOF)
		synctest.Wait()

		// The pipe should propagate the error.
		buf := make([]byte, 1)
		_, err := r.Read(buf)
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("got err=%v, want %v", err, io.ErrUnexpectedEOF)
		}
	})
}

func TestPipeStdinMultipleBytesPushedBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		underlying, underlyingW := io.Pipe()
		var pushed []byte
		r, stop := pipeStdin(underlying, func(data []byte) {
			pushed = append(pushed, data...)
		})

		// Drain the pipe.
		go io.Copy(io.Discard, r)

		go underlyingW.Write([]byte("a"))
		synctest.Wait()

		stop()
		synctest.Wait()

		go underlyingW.Write([]byte("xyz"))
		synctest.Wait()

		if string(pushed) != "xyz" {
			t.Fatalf("pushed = %q, want %q", pushed, "xyz")
		}

		underlyingW.Close()
	})
}

// TestDirectCopyLosesKeystroke demonstrates the bug that pipeStdin fixes.
// It simulates the exact x/crypto/ssh behavior: an internal goroutine
// does io.Copy(channel, session.Stdin). When the channel closes (remote
// shell exits), the goroutine is still blocked on stdin.Read(). The next
// byte it reads gets written to the closed channel and silently discarded.
func TestDirectCopyLosesKeystroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, sessionW := io.Pipe()  // shell session (user keystrokes)
		channelR, channelW := io.Pipe() // SSH channel to remote shell

		// Simulate x/crypto/ssh's internal goroutine.
		copyDone := make(chan struct{})
		go func() {
			defer close(copyDone)
			io.Copy(channelW, session)
			channelW.Close()
		}()

		// Deliver "hello" through the channel.
		go sessionW.Write([]byte("hello"))
		buf := make([]byte, 5)
		if _, err := io.ReadFull(channelR, buf); err != nil || string(buf) != "hello" {
			t.Fatalf("got %q err=%v", buf, err)
		}

		// Remote shell exits — close the channel reader.
		// The io.Copy goroutine is blocked on session.Read(); it hasn't
		// noticed the channel is dead yet.
		channelR.Close()
		synctest.Wait()

		// User types "x". The goroutine reads it, tries channelW.Write,
		// fails (channelR closed → ErrClosedPipe), and exits — discarding "x".
		go sessionW.Write([]byte("x"))
		synctest.Wait()
		<-copyDone

		// "x" is gone. Nobody pushed it back. There is no way to recover it.
		// (This is the bug pipeStdin fixes.)
		sessionW.Close()
	})
}

// TestPipeStdinPreservesKeystroke is the companion to TestDirectCopyLosesKeystroke.
// Same scenario, but with pipeStdin mediating the reads. The byte is preserved.
func TestPipeStdinPreservesKeystroke(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, sessionW := io.Pipe()
		channelR, channelW := io.Pipe()

		var pushed []byte
		stdin, stop := pipeStdin(session, func(data []byte) {
			pushed = append(pushed, data...)
		})

		// Simulate x/crypto/ssh's internal goroutine.
		copyDone := make(chan struct{})
		go func() {
			defer close(copyDone)
			io.Copy(channelW, stdin)
			channelW.Close()
		}()

		// Deliver "hello".
		go sessionW.Write([]byte("hello"))
		buf := make([]byte, 5)
		if _, err := io.ReadFull(channelR, buf); err != nil || string(buf) != "hello" {
			t.Fatalf("got %q err=%v", buf, err)
		}

		// Remote shell exits — close the channel reader.
		channelR.Close()
		synctest.Wait()

		// Stop the bridge. This closes both pipe ends:
		// - The io.Copy goroutine sees EOF on stdin (pr) and exits.
		// - The pipeStdin goroutine remains blocked on session.Read().
		stop()
		synctest.Wait()
		<-copyDone

		// User types "x". The pipeStdin goroutine reads it, can't write
		// to the closed pipe, and pushes it back — preserving the byte.
		go sessionW.Write([]byte("x"))
		synctest.Wait()

		if string(pushed) != "x" {
			t.Fatalf("pushed = %q, want %q — keystroke was lost!", pushed, "x")
		}

		sessionW.Close()
	})
}
