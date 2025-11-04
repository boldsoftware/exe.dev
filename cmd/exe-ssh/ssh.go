package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/logging"
	"github.com/creack/pty"

	"exe.dev/exelet/utils"
)

func startSSHServer(addr, hostKeyPath, authorizedKeysPath string) error {
	slog.Info("starting ssh server", "addr", addr)
	// server
	opts := []ssh.Option{
		wish.WithAddress(addr),
		wish.WithHostKeyPath(hostKeyPath),
	}

	// add authorized keys if exists
	if _, err := os.Stat(authorizedKeysPath); err == nil {
		slog.Info("using authorized keys", "path", authorizedKeysPath)
		opts = append(opts, wish.WithAuthorizedKeys(authorizedKeysPath))
	}

	// standard
	opts = append(opts,
		wish.WithMiddleware(
			func(next ssh.Handler) ssh.Handler {
				return func(s ssh.Session) {
					ptyReq, winCh, isPty := s.Pty()
					if !isPty {
						s.Write([]byte("no PTY requested\n"))
						s.Exit(1)
						return
					}
					// start shell
					shellPath, err := utils.GetShellPath()
					if err != nil {
						s.Write([]byte(err.Error() + "\n"))
						s.Exit(1)
						return
					}

					slog.Info("using shell", "path", shellPath)

					cmd := exec.Command(shellPath)
					cmd.Env = []string{
						"TERM=xterm",
						"PWD=/",
						"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
						"HOME=/",
					}
					f, err := pty.StartWithSize(cmd, &pty.Winsize{
						Cols: uint16(ptyReq.Window.Width),
						Rows: uint16(ptyReq.Window.Height),
					})
					if err != nil {
						s.Write([]byte(err.Error()))
						s.Exit(1)
						return
					}
					defer f.Close()

					// io
					go func() { _, _ = io.Copy(f, s) }()
					go func() { _, _ = io.Copy(s, f) }()

					// handle window resize events
					go func() {
						for win := range winCh {
							pty.Setsize(f, &pty.Winsize{
								Cols: uint16(win.Width),
								Rows: uint16(win.Height),
							})
						}
					}()

					cmd.Wait()
					next(s)
				}
			},
			logging.Middleware(),
		),
	)
	srv, err := wish.NewServer(opts...)
	if err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error)
	doneCh := make(chan bool, 1)

	go func() {
		for sig := range signals {
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("shutting down")
				if err := srv.Close(); err != nil {
					slog.Error(err.Error())
				}
				doneCh <- true
			default:
				slog.Warn("unhandled signal", "signal", sig)
			}
		}
	}()

	go func() {
		if err = srv.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-doneCh:
	case err := <-errCh:
		return err
	}

	return nil
}
