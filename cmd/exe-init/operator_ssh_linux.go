//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/mdlayher/vsock"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/ssh"

	"exe.dev/exelet/config"
	"exe.dev/exelet/utils"
)

// OperatorSSHVsockPort is the AF_VSOCK port exe-init's operator SSH server
// listens on inside the guest. Operators reach it by speaking the
// cloud-hypervisor hybrid-vsock handshake ("CONNECT <port>\n") on the
// unix-domain socket CH binds on the host.
const OperatorSSHVsockPort = 2222

// startOperatorSSH launches a child process that runs the vsock SSH server.
//
// We fork rather than start a goroutine because exe-init execs the guest's
// init system (e.g. systemd) once boot plumbing is ready, which replaces the
// process image and would kill any in-process listener. The child inherits
// nothing it needs from us other than the executable on disk and the
// readable host-key / authorized-keys files.
//
// Best-effort: any failure here is logged and boot continues.
func startOperatorSSH() {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("operator-ssh: os.Executable", "err", err)
		return
	}
	if _, err := os.Stat(config.InstanceSSHHostKeyPath); err != nil {
		slog.Warn("operator-ssh: host key missing; not starting", "path", config.InstanceSSHHostKeyPath, "err", err)
		return
	}
	if _, err := os.Stat(config.InstanceSSHPublicKeysPath); err != nil {
		slog.Warn("operator-ssh: authorized_keys missing; not starting", "path", config.InstanceSSHPublicKeysPath, "err", err)
		return
	}

	cmd := exec.Command(exe, "op-ssh")
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		slog.Warn("operator-ssh: spawn child", "err", err)
		return
	}
	slog.Info("operator-ssh: spawned child", "pid", cmd.Process.Pid)
	_ = cmd.Process.Release()
}

// runOperatorSSHAction is the hidden `exe-init op-ssh` subcommand. It reads
// the same host key and authorized_keys the in-guest sshd uses, binds an
// AF_VSOCK listener, and serves SSH on it using golang.org/x/crypto/ssh
// directly. Sessions exec a login shell.
func runOperatorSSHAction(_ *cli.Context) error {
	hostKeyBytes, err := os.ReadFile(config.InstanceSSHHostKeyPath)
	if err != nil {
		return err
	}
	hostSigner, err := ssh.ParsePrivateKey(hostKeyBytes)
	if err != nil {
		return err
	}

	authKeysBytes, err := os.ReadFile(config.InstanceSSHPublicKeysPath)
	if err != nil {
		return err
	}
	authKeys := parseAuthorizedKeys(authKeysBytes)
	if len(authKeys) == 0 {
		slog.Warn("operator-ssh: no authorized keys; exiting")
		return nil
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			km := key.Marshal()
			for _, k := range authKeys {
				if bytes.Equal(km, k.Marshal()) {
					return nil, nil
				}
			}
			return nil, io.EOF // any non-nil error rejects
		},
	}
	cfg.AddHostKey(hostSigner)

	lis, err := vsock.Listen(OperatorSSHVsockPort, nil)
	if err != nil {
		return err
	}
	slog.Info("operator-ssh: listening", "vsock-port", OperatorSSHVsockPort, "authorized-keys", len(authKeys))

	for {
		conn, err := lis.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("operator-ssh: panic in connection handler", "panic", r)
				}
			}()
			serveOperatorSSHConn(conn, cfg)
		}()
	}
}

// operatorSSHHandshakeTimeout bounds how long we'll wait for a client to
// complete the SSH handshake. Keeps a dead/slow peer from pinning a
// goroutine forever.
const operatorSSHHandshakeTimeout = 30 * time.Second

func serveOperatorSSHConn(nConn net.Conn, cfg *ssh.ServerConfig) {
	defer nConn.Close()
	// Bound the handshake; clear the deadline once it's done so the session
	// can be long-lived.
	_ = nConn.SetDeadline(time.Now().Add(operatorSSHHandshakeTimeout))
	sConn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		slog.Warn("operator-ssh: handshake", "err", err)
		return
	}
	defer sConn.Close()
	_ = nConn.SetDeadline(time.Time{})
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			slog.Warn("operator-ssh: accept channel", "err", err)
			continue
		}
		go handleSession(ch, chReqs)
	}
}

// handleSession implements the SSH session state machine: collect env /
// pty-req, then react to "shell" or "exec" by launching a process, then
// handle window-change while it runs.
func handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	var (
		env       []string
		term      string
		winCols   uint32
		winRows   uint32
		havePty   bool
		ptyF      *os.File
		started   bool // RFC 4254 §6.5: only one shell/exec/subsystem per channel.
		startedCh = make(chan struct{})
	)

	for req := range reqs {
		switch req.Type {
		case "env":
			var p struct{ Name, Value string }
			if err := ssh.Unmarshal(req.Payload, &p); err == nil {
				env = append(env, p.Name+"="+p.Value)
			}
			req.Reply(true, nil)
		case "pty-req":
			if started {
				req.Reply(false, nil)
				continue
			}
			t, cols, rows, ok := parsePtyReq(req.Payload)
			if !ok {
				req.Reply(false, nil)
				continue
			}
			term, winCols, winRows, havePty = t, cols, rows, true
			req.Reply(true, nil)
		case "window-change":
			cols, rows, ok := parseWinCh(req.Payload)
			if ok && ptyF != nil {
				_ = pty.Setsize(ptyF, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
		case "shell", "exec":
			if started {
				// Client is buggy or hostile; RFC says reject.
				req.Reply(false, nil)
				continue
			}
			var rawCmd string
			if req.Type == "exec" {
				var p struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					req.Reply(false, nil)
					continue
				}
				rawCmd = p.Command
			}
			c, f, err := startOperatorShell(rawCmd, env, havePty, term, winCols, winRows, ch)
			if err != nil {
				slog.Warn("operator-ssh: start shell", "err", err)
				req.Reply(false, nil)
				continue
			}
			ptyF = f
			started = true
			req.Reply(true, nil)
			go func() {
				defer close(startedCh)
				waitAndExit(c, f, ch)
			}()
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
	if started {
		<-startedCh
	}
}

func startOperatorShell(rawCmd string, extraEnv []string, havePty bool, term string, cols, rows uint32, ch ssh.Channel) (*exec.Cmd, *os.File, error) {
	shellPath, err := utils.GetShellPath()
	if err != nil || shellPath == "" {
		shellPath = "/exe.dev/bin/sh"
	}
	var cmd *exec.Cmd
	if rawCmd != "" {
		cmd = exec.Command(shellPath, "-c", rawCmd)
	} else {
		cmd = exec.Command(shellPath, "-l")
	}
	cmd.Env = append(cmd.Env,
		"PATH=/exe.dev/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"PWD=/root",
		"USER=root",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	if havePty {
		cmd.Env = append(cmd.Env, "TERM="+term)
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
		if err != nil {
			return nil, nil, err
		}
		go func() { _, _ = io.Copy(f, ch) }()
		go func() { _, _ = io.Copy(ch, f) }()
		return cmd, f, nil
	}
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, nil, nil
}

func waitAndExit(cmd *exec.Cmd, ptyF *os.File, ch ssh.Channel) {
	err := cmd.Wait()
	if ptyF != nil {
		_ = ptyF.Close()
	}
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	payload := struct{ Status uint32 }{Status: uint32(code)}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&payload))
	_ = ch.Close()
}

// parsePtyReq parses an RFC 4254 "pty-req" payload:
//
//	string    TERM
//	uint32    width (characters)
//	uint32    height (rows)
//	uint32    width (pixels)
//	uint32    height (pixels)
//	string    encoded terminal modes
func parsePtyReq(payload []byte) (term string, cols, rows uint32, ok bool) {
	buf := payload
	term, buf, ok = readString(buf)
	if !ok {
		return term, cols, rows, ok
	}
	if len(buf) < 16 {
		ok = false
		return term, cols, rows, ok
	}
	cols = binary.BigEndian.Uint32(buf[0:4])
	rows = binary.BigEndian.Uint32(buf[4:8])
	return term, cols, rows, true
}

// parseWinCh parses an RFC 4254 "window-change" payload:
//
//	uint32 width, uint32 height, uint32 wpx, uint32 hpx
func parseWinCh(payload []byte) (cols, rows uint32, ok bool) {
	if len(payload) < 16 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(payload[0:4]), binary.BigEndian.Uint32(payload[4:8]), true
}

func readString(buf []byte) (string, []byte, bool) {
	if len(buf) < 4 {
		return "", buf, false
	}
	n := binary.BigEndian.Uint32(buf[:4])
	if uint32(len(buf)-4) < n {
		return "", buf, false
	}
	return string(buf[4 : 4+n]), buf[4+n:], true
}

func parseAuthorizedKeys(data []byte) []ssh.PublicKey {
	var out []ssh.PublicKey
	rest := data
	for len(rest) > 0 {
		k, _, _, r, err := ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		out = append(out, k)
		rest = r
	}
	return out
}
