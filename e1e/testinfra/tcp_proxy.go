package testinfra

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// TCPProxy creats an arbitrary TCP port on the local host,
// and proxies data for all connections to that port to a destination address.
// The destination address need not be known when the TCPProxy
// is created.
//
// This permits handling TCP connections to a program that hasn't started yet.
// This is needed when setting up a test environment, as some processes
// need to know each other's address but we have to start one first.
// In production this isn't an issue because we start them with
// known, fixed, addresses and port numbers.
//
// TODO: figure out why we're seeing connections before SetDestPort is called,
// and stop doing that.
type TCPProxy struct {
	Name     string                      // an arbitrary name used in logs
	listener *net.TCPListener            // where to listen for connections
	address  *net.TCPAddr                // listening address
	dst      atomic.Pointer[net.TCPAddr] // destination to connect to
	ch       chan bool                   // channel closed when dst set
	cancel   context.CancelFunc          // closes connections
	latency  time.Duration               // latency to add to each read
}

// NewTCPProxy returns a new TCPProxy. This creates a new listener.
// The destination address is not set; all incoming connections
// will delay until it is set by calling [TCPProxy.SetDestPort].
func NewTCPProxy(name string) (*TCPProxy, error) {
	ln, err := net.ListenTCP("tcp", nil)
	if err != nil {
		return nil, err
	}
	address := ln.Addr().(*net.TCPAddr)
	ret := &TCPProxy{
		Name:     name,
		listener: ln,
		address:  address,
		ch:       make(chan bool),
	}
	return ret, nil
}

// Address return the address on the local host on which the proxy is listening.
func (p *TCPProxy) Address() *net.TCPAddr {
	return p.address
}

// Port returns the TCP port on the local host on which the proxy is listening.
func (p *TCPProxy) Port() int {
	return p.address.Port
}

// Serve starts listening for connections and proxying them.
// This should normally be called in a new goroutine.
// This starts other goroutines.
// Errors are logged via slog.
func (p *TCPProxy) Serve(ctx context.Context) {
	if p.cancel != nil {
		slog.ErrorContext(ctx, "TCPProxy Serve method called more than once", "name", p.Name)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	var wg sync.WaitGroup

	for {
		c, err := p.listener.AcceptTCP()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.WarnContext(ctx, "TCPProxy accept error", "name", p.Name, "addr", p.listener.Addr().String(), "error", err)
				p.listener.Close()
			}
			cancel()
			wg.Wait()
			return
		}

		wg.Add(1)
		go func(c *net.TCPConn) {
			defer wg.Done()
			p.proxy(ctx, c)
		}(c)
	}
}

// proxy proxies a new connection to the destionation address.
func (p *TCPProxy) proxy(ctx context.Context, c *net.TCPConn) {
	defer c.Close()

	// Wait for destination address to be set.
	<-p.ch

	dstAddr := p.dst.Load()
	dst, err := net.DialTCP("tcp", nil, dstAddr)
	if err != nil {
		slog.ErrorContext(ctx, "TCPProxy: failed to connect to dst", "name", p.Name, "address", dstAddr, "error", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(3)

	copyDone := make(chan bool, 2)

	cp := io.Copy
	if p.latency > 0 {
		cp = p.copyWithLatency
	}

	copyData := func(to, from *net.TCPConn) {
		defer func() {
			copyDone <- true
			wg.Done()
		}()
		if _, err := cp(to, from); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.WarnContext(ctx, "TCPProxy copying error", "name", p.Name, "to", to.LocalAddr().String(), "from", from.LocalAddr().String(), "error", err)
		}
	}

	go copyData(dst, c)
	go copyData(c, dst)

	go func() {
		defer wg.Done()
		select {
		case <-copyDone:
			// One direction finished. Close both connections
			// to unblock the other direction's io.Copy.
			c.Close()
			dst.Close()
			<-copyDone
		case <-ctx.Done():
			// Context cancelled. Close both connections
			// to unblock both io.Copy goroutines.
			c.Close()
			dst.Close()
			<-copyDone
			<-copyDone
		}
	}()

	wg.Wait()
}

// SetDestPort sets the destination address to port on localhost.
func (p *TCPProxy) SetDestPort(port int) {
	addr := &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}
	p.dst.Store(addr)
	close(p.ch)
}

// SetLatency sets a latency to add to each read operation.
// This must be called before Serve.
func (p *TCPProxy) SetLatency(d time.Duration) {
	p.latency = d
}

// copyWithLatency copies from src to dst, adding p.latency after each read.
func (p *TCPProxy) copyWithLatency(dst io.Writer, src io.Reader) (int64, error) {
	var written int64
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			time.Sleep(p.latency)
			nw, werr := dst.Write(buf[:n])
			written += int64(nw)
			if werr != nil {
				return written, werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return written, nil
			}
			return written, err
		}
	}
}

// Close closes the listening socket.
func (p *TCPProxy) Close() {
	p.listener.Close()
	if p.cancel != nil {
		p.cancel()
	}
}
