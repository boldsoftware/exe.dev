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
