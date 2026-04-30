//go:build linux

package main

import (
	"log/slog"
	"net"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/urfave/cli/v2"
)

// startMemd spawns the in-guest memd child process and detaches it.
// Best-effort — failures are logged and boot continues. v0 spawns
// once; reaping is handled by PID-1.
func startMemd() {
	spawnDetachedSubcommand("memd", "memd")
}

// memdMaxConcurrent caps in-flight memd handlers. The host scraper is a
// single goroutine per VM so legitimate traffic uses at most one slot.
// The cap exists only to keep an in-guest attacker from fork-bombing
// exe-init by hammering Accept().
const memdMaxConcurrent = 16

// runMemdAction is the hidden `exe-init memd` subcommand.
func runMemdAction(_ *cli.Context) error {
	lis, err := vsock.Listen(MemdVsockPort, nil)
	if err != nil {
		return err
	}
	slog.Info("memd: listening", "vsock-port", MemdVsockPort)
	sem := make(chan struct{}, memdMaxConcurrent)
	for {
		conn, err := lis.Accept()
		if err != nil {
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			// Pool full. Drop the connection rather than queue
			// without bound. Legitimate traffic is one connection
			// at a time; this only triggers under abuse.
			slog.Warn("memd: dropping connection (handler pool full)")
			_ = conn.Close()
			continue
		}
		go func(c net.Conn) {
			defer func() {
				<-sem
				if r := recover(); r != nil {
					slog.Error("memd: panic", "panic", r)
				}
			}()
			if err := serveMemdConn(c, time.Now, c.SetDeadline); err != nil {
				slog.Debug("memd: serve", "err", err)
			}
		}(conn)
	}
}
