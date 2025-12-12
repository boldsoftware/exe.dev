package ctrlc

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
)

func TestCtrlC(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, _, _ := WithReader(context.Background(), pr)

	go pw.Write([]byte{3})

	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), ErrCanceled) {
		t.Errorf("expected ErrCanceled, got %v", context.Cause(ctx))
	}
}

func TestStopPreservesByte(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, r, stop := WithReader(context.Background(), pr)

	stop()
	go pw.Write([]byte{'x'})

	buf := make([]byte, 1)
	n, err := r.Read(buf)
	if err != nil || n != 1 || buf[0] != 'x' {
		t.Errorf("expected 'x', got %q (n=%d, err=%v)", buf[:n], n, err)
	}

	select {
	case <-ctx.Done():
		t.Error("context should not be canceled")
	default:
	}
}

func TestReadError(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, _, _ := WithReader(context.Background(), pr)

	go pw.CloseWithError(io.ErrUnexpectedEOF)

	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), io.ErrUnexpectedEOF) {
		t.Errorf("expected ErrUnexpectedEOF, got %v", context.Cause(ctx))
	}
}

func TestBytesBeforeAndAfterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		_, r, stop := WithReader(context.Background(), pr)

		// Write 'a' before stop - goroutine reads it, writes to pipe.
		go pw.Write([]byte{'a'})
		buf := make([]byte, 1)
		if _, err := r.Read(buf); err != nil || buf[0] != 'a' {
			t.Fatalf("expected 'a', got %q, err=%v", buf, err)
		}

		// Now stop. Next byte triggers goroutine exit after writing to pipe.
		stop()
		go pw.Write([]byte{'b'})

		if _, err := r.Read(buf); err != nil || buf[0] != 'b' {
			t.Fatalf("expected 'b', got %q, err=%v", buf, err)
		}

		// Subsequent reads go directly to underlying reader.
		go pw.Write([]byte{'c'})
		if _, err := r.Read(buf); err != nil || buf[0] != 'c' {
			t.Fatalf("expected 'c', got %q, err=%v", buf, err)
		}
	})
}

func TestFirstByteAfterStopNotDiscarded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		_, r, stop := WithReader(context.Background(), pr)

		// Write "ls\n" and read it back.
		go pw.Write([]byte("ls\n"))
		buf := make([]byte, 3)
		if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "ls\n" {
			t.Fatalf("expected %q, got %q, err=%v", "ls\n", buf, err)
		}

		// Write "exit\n" and read it back.
		go pw.Write([]byte("exit\n"))
		buf = make([]byte, 5)
		if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "exit\n" {
			t.Fatalf("expected %q, got %q, err=%v", "exit\n", buf, err)
		}

		// Stop while goroutine is blocked waiting for more input.
		synctest.Wait()
		stop()

		// Write "hi" after stop. Goroutine reads 'h', writes to internal
		// pipe, exits. 'i' remains in underlying reader.
		go pw.Write([]byte("hi"))

		// Wait for goroutine to exit (it will write 'h' to internal pipe
		// and close it).
		synctest.Wait()

		// Now try to read. The internal pipe has 'h' and is closed.
		// After reading 'h', MultiReader should switch to underlying
		// reader and get 'i'.
		buf = make([]byte, 2)
		n, err := io.ReadFull(r, buf)
		if err != nil {
			t.Fatalf("ReadFull error: %v", err)
		}
		if n != 2 || string(buf) != "hi" {
			t.Fatalf("expected %q, got %q (n=%d)", "hi", buf[:n], n)
		}
	})
}

