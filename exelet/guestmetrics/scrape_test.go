package guestmetrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeDial wires Pool.scrapeOnce to a net.Pipe so tests can drive the
// memd-side of the conversation.
func pipeDial(server func(net.Conn)) DialFunc {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go func() {
			defer c2.Close()
			server(c2)
		}()
		return c1, nil
	}
}

func TestScrapeOnceRejectsOversizedPayload(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	wg.Add(1)
	p := NewPool(PoolConfig{
		DialFunc: pipeDial(func(c net.Conn) {
			defer wg.Done()
			// Drain the request first.
			rbuf := make([]byte, 64)
			_, _ = c.Read(rbuf)
			// Stream bytes without a terminating newline up to the
			// cap. The cap on the read side is 256 KiB; write more
			// than that.
			buf := strings.Repeat("x", 4096)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := c.Write([]byte(buf)); err != nil {
					return
				}
			}
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.scrapeOnce(ctx, VMInfo{ID: "v"})
	wg.Wait()
	if err == nil {
		t.Fatalf("expected error from oversized payload, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestScrapeOnceRejectsBadProtocolVersion(t *testing.T) {
	t.Parallel()
	p := NewPool(PoolConfig{
		DialFunc: pipeDial(func(c net.Conn) {
			// Read the request, then write a JSON sample with a
			// version that will never match ProtocolVersion.
			buf := make([]byte, 64)
			_, _ = c.Read(buf)
			_, _ = fmt.Fprintf(c, `{"version":999,"meminfo":{"MemTotal":1024},"uptime_sec":1}`+"\n")
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.scrapeOnce(ctx, VMInfo{ID: "v"})
	if err == nil {
		t.Fatalf("expected error for unsupported protocol version, got nil")
	}
	if !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScrapeOnceAcceptsValidVersion(t *testing.T) {
	t.Parallel()
	p := NewPool(PoolConfig{
		DialFunc: pipeDial(func(c net.Conn) {
			buf := make([]byte, 64)
			_, _ = c.Read(buf)
			_, _ = fmt.Fprintf(c, `{"version":%d,"meminfo":{"MemTotal":2048},"uptime_sec":1}`+"\n", ProtocolVersion)
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := p.scrapeOnce(ctx, VMInfo{ID: "v"})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if raw.Version != ProtocolVersion {
		t.Fatalf("got version %d, want %d", raw.Version, ProtocolVersion)
	}
}

var _ = errors.New // ensure errors import is referenced if test trims
