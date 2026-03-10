package exepipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"syscall"

	"exe.dev/exepipe/internal/cmds"
)

// piping manages piping data between file descriptors.
type piping struct {
	pipeInstance *PipeInstance

	connsMu sync.Mutex
	conns   map[net.Conn]struct{}

	listenersMu sync.Mutex
	listeners   map[string]listener
}

// listener is the information we keep for a listener.
type listener struct {
	info cmds.Listener
	ln   net.Listener
}

// setupPiping sets up a new [piping] instance.
func setupPiping(cfg *PipeConfig, pi *PipeInstance) (*piping, error) {
	ret := &piping{
		pipeInstance: pi,
		conns:        make(map[net.Conn]struct{}),
		listeners:    make(map[string]listener),
	}
	return ret, nil
}

// start starts piping. There is currently nothing to do.
func (p *piping) start(ctx context.Context) error {
	return nil
}

// stop stops piping.
func (p *piping) stop(ctx context.Context) {
	p.connsMu.Lock()
	defer p.connsMu.Unlock()

	for conn := range p.conns {
		conn.Close()
	}
	clear(p.conns)
}

// addConn adds a network connection to the piping map.
func (p *piping) addConn(conn net.Conn) {
	p.connsMu.Lock()
	defer p.connsMu.Unlock()
	p.addConnLocked(conn)
}

// addConnLocked adds a network connection to the piping map,
// assuming that connsMu is held.
func (p *piping) addConnLocked(conn net.Conn) {
	if _, ok := p.conns[conn]; ok {
		panic("duplicate exepipe piping Conn")
	}
	p.conns[conn] = struct{}{}
}

// rmConn removes a network connection from the piping map.
func (p *piping) rmConn(conn net.Conn) {
	p.connsMu.Lock()
	defer p.connsMu.Unlock()
	p.rmConnLocked(conn)
}

// rmConnLocked removes a network connection from the piping map,
// assuming that connsMu is held.
func (p *piping) rmConnLocked(conn net.Conn) {
	delete(p.conns, conn)
}

// connsCount returns the number of network connections being handled,
// for testing purposes.
func (p *piping) connsCount() int {
	p.connsMu.Lock()
	defer p.connsMu.Unlock()
	return len(p.conns)
}

// Copy takes two socket file descriptors and starts goroutines to
// copy data between them.
func (p *piping) Copy(ctx context.Context, fd1, fd2 int, typ string) {
	go p.doCopy(ctx, fd1, fd2, typ)
}

// doCopy implements Copy, running in a separate goroutine.
func (p *piping) doCopy(ctx context.Context, fd1, fd2 int, typ string) {
	f := os.NewFile(uintptr(fd1), p.sockname(ctx, fd1))
	c1, err := net.FileConn(f)
	f.Close()
	if err != nil {
		syscall.Close(fd2)
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe copy: net.FileConn failed", "fd", fd1, "name", p.sockname(ctx, fd1), "type", typ, "error", err)
		return
	}

	f = os.NewFile(uintptr(fd2), p.sockname(ctx, fd2))
	c2, err := net.FileConn(f)
	f.Close()
	if err != nil {
		c1.Close()
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe copy: net.FileConn failed", "fd", fd2, "name", p.sockname(ctx, fd2), "type", typ, "error", err)
		return
	}

	p.copyConns(ctx, c1, c2, typ)
}

// copyConns copies data between two net.Conn values.
// This is the core operation of exepipe.
func (p *piping) copyConns(ctx context.Context, c1, c2 net.Conn, typ string) {
	p.pipeInstance.metrics.sessionsTotal.WithLabelValues(typ).Inc()
	p.pipeInstance.metrics.sessionsInFlight.WithLabelValues(typ).Inc()
	defer p.pipeInstance.metrics.sessionsInFlight.WithLabelValues(typ).Dec()

	defer func() {
		p.connsMu.Lock()
		p.rmConnLocked(c1)
		p.rmConnLocked(c2)
		p.connsMu.Unlock()

		c1.Close()
		c2.Close()
	}()

	p.connsMu.Lock()
	p.addConnLocked(c1)
	p.addConnLocked(c2)
	p.connsMu.Unlock()

	copy := func(to, from net.Conn) {
		n, err := io.Copy(to, from)
		p.pipeInstance.metrics.bytesTotal.WithLabelValues(typ).Add(float64(n))
		if err != nil && !errors.Is(err, net.ErrClosed) {
			p.pipeInstance.lg.WarnContext(ctx, "exepipe copy error", "type", typ, "error", err)
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() { copy(c1, c2) })
	wg.Go(func() { copy(c2, c1) })
	wg.Wait()
}

// sockname returns the local address of the socket, as a string.
// Errors are logged but not returned.
func (p *piping) sockname(ctx context.Context, fd int) string {
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe: getsockname failed", "fd", fd, "error", err)
		return fmt.Sprintf("fd%d", fd)
	}

	switch sa := sa.(type) {
	case *syscall.SockaddrInet4:
		ip := netip.AddrFrom4(sa.Addr)
		return net.JoinHostPort(ip.String(), strconv.Itoa(sa.Port))
	case *syscall.SockaddrInet6:
		ip := netip.AddrFrom16(sa.Addr)
		s := net.JoinHostPort(ip.String(), strconv.Itoa(sa.Port))
		if sa.ZoneId != 0 {
			s += fmt.Sprintf("%%%d", sa.ZoneId)
		}
		return s
	case *syscall.SockaddrUnix:
		return sa.Name
	default:
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe: unrecognized socket address type", "type", fmt.Sprintf("%T", sa))
		return fmt.Sprintf("fd%d", fd)
	}
}

// Listen takes a socket file descriptor and starts goroutines
// to listen on that socket and start copying between the
// accepted socket and the TCP address.
func (p *piping) Listen(ctx context.Context, key string, fd int, host string, port int, typ string) {
	f := os.NewFile(uintptr(fd), p.sockname(ctx, fd))
	ln, err := net.FileListener(f)
	f.Close()
	if err != nil {
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe listen: net.FileListener failed", "fd", fd, "name", p.sockname(ctx, fd), "error", err)
		return
	}

	p.addListener(ctx, key, ln, host, port, typ)

	go p.doListen(ctx, key, ln, host, port, typ)
}

// doListen implements Listen, running in a separate goroutine.
func (p *piping) doListen(ctx context.Context, key string, ln net.Listener, host string, port int, typ string) {
	defer p.rmListener(ctx, key)
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				p.pipeInstance.lg.WarnContext(ctx, "exepipe listen error", "type", typ, "error", err)
			}
			return
		}

		go p.connect(ctx, conn, host, port, typ)
	}
}

// Unlisten closes an existing listener.
func (p *piping) Unlisten(ctx context.Context, key string) error {
	p.listenersMu.Lock()
	defer p.listenersMu.Unlock()

	info, ok := p.listeners[key]
	if !ok {
		return fmt.Errorf("exepipe: request to remove non-existent listener %q", key)
	}

	info.ln.Close()
	delete(p.listeners, key)

	return nil
}

// connect opens a connection to host/port, and starts copying from conn.
// This runs in a separate goroutine.
func (p *piping) connect(ctx context.Context, conn1 net.Conn, host string, port int, typ string) {
	conn2, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		conn1.Close()
		p.pipeInstance.lg.ErrorContext(ctx, "exepipe failed to connect", "host", host, "port", port, "type", typ, "error", err)
		return
	}

	p.copyConns(ctx, conn1, conn2, typ)
}

// addListener records a new listener.
func (p *piping) addListener(ctx context.Context, key string, ln net.Listener, host string, port int, typ string) {
	p.listenersMu.Lock()
	defer p.listenersMu.Unlock()

	if old, ok := p.listeners[key]; ok {
		if host != old.info.Host || port != old.info.Port || typ != old.info.Type {
			p.pipeInstance.lg.WarnContext(ctx, "exepipe: listener key changed", "key", key, "oldHost", old.info.Host, "oldPort", old.info.Port, "oldType", old.info.Type, "newHost", host, "newPort", port, "newType", typ)
		}
	}

	lnInfo := listener{
		info: cmds.Listener{
			Key:  key,
			Host: host,
			Port: port,
			Type: typ,
		},
		ln: ln,
	}
	p.listeners[key] = lnInfo
}

// rmListener removes a listener.
func (p *piping) rmListener(ctx context.Context, key string) {
	p.listenersMu.Lock()
	defer p.listenersMu.Unlock()

	delete(p.listeners, key)
}

// listeners returns an iterator over all the listeners.
// This will keep the map locked during the iteration.
// This is OK because this command is only expected to be used
// once at startup time.
func (p *piping) allListeners() iter.Seq[cmds.Listener] {
	return func(yield func(cmds.Listener) bool) {
		p.listenersMu.Lock()
		defer p.listenersMu.Unlock()

		for _, ln := range p.listeners {
			if !yield(ln.info) {
				return
			}
		}
	}
}
