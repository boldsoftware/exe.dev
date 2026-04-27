// Operator-SSH helpers: dial the per-VM hybrid-vsock unix socket exposed by
// cloud-hypervisor, perform the "CONNECT <port>" handshake, and run the SSH
// handshake on top. Works for both local and remote exelets.

package testinfra

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
	"golang.org/x/crypto/ssh"
)

// OperatorSSHVsockPort mirrors cmd/exe-init's OperatorSSHVsockPort. Hard-coded
// in the test to avoid importing guest-side init code from test infra.
const OperatorSSHVsockPort = 2222

// OperatorSSHSocketPath returns the on-host unix-domain socket path that
// cloud-hypervisor binds for the given instance's operator-SSH vsock. Mirrors
// exelet/vmm/cloudhypervisor.(*VMM).OperatorSSHSocketPath.
func OperatorSSHSocketPath(dataDir, instanceID string) string {
	return filepath.Join(dataDir, "runtime", instanceID, "opssh.sock")
}

// DialOperatorSSH opens a net.Conn to the in-guest operator SSH server via
// cloud-hypervisor's hybrid-vsock. The returned cleanup func must be called
// to tear down the proxy process.
//
// Local exelets (RemoteHost == "") use `sudo socat`; remote exelets tunnel
// through ssh to reach the CH unix socket.
func DialOperatorSSH(ctx context.Context, exelet *ExeletInstance, instanceID string) (net.Conn, func(), error) {
	socketPath := OperatorSSHSocketPath(exelet.DataDir, instanceID)

	dialCtx, cancel := context.WithCancel(ctx)
	var cmd *exec.Cmd
	if exelet.RemoteHost == "" {
		cmd = exec.CommandContext(dialCtx, "sudo", "socat", "-t30", "-", "UNIX-CONNECT:"+socketPath)
	} else {
		// Tunnel through ssh. socat on the remote side bridges its stdio to
		// the unix socket; ssh bridges our stdio to socat's stdio.
		cmd = exec.CommandContext(dialCtx, "ssh",
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+sshControlPath(exelet.RemoteHost),
			"-o", "ControlPersist=60s",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			exelet.RemoteHost,
			"sudo socat -t30 - UNIX-CONNECT:"+socketPath)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &opSSHStderr{b: &stderrBuf}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, err
	}

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			_ = stdin.Close()
			cancel()
			_ = cmd.Wait()
		})
	}

	if _, err := fmt.Fprintf(stdin, "CONNECT %d\n", OperatorSSHVsockPort); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write CONNECT: %w", err)
	}
	br := bufio.NewReader(stdout)
	line, err := br.ReadString('\n')
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("read OK: %w (stderr=%q)", err, stderrBuf.String())
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "OK") {
		cleanup()
		return nil, nil, fmt.Errorf("unexpected CH vsock response: %q (stderr=%q)", line, stderrBuf.String())
	}

	return &opSSHConn{r: br, w: stdin, closer: cleanup}, cleanup, nil
}

// OperatorSSHClient dials operator SSH and performs the SSH handshake,
// returning an *ssh.Client. Retries for up to `timeout` while exe-init is
// still coming up (or after migration while the receiving exelet is still
// wiring up the CH socket).
func OperatorSSHClient(ctx context.Context, exelet *ExeletInstance, instanceID string, signer ssh.Signer, timeout time.Duration) (*ssh.Client, error) {
	var lastErr error
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, cleanup, err := DialOperatorSSH(ctx, exelet, instanceID)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		cfg := &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         15 * time.Second,
		}
		c, chans, reqs, err := ssh.NewClientConn(conn, "vsock", cfg)
		if err != nil {
			cleanup()
			lastErr = fmt.Errorf("ssh handshake: %w", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return ssh.NewClient(c, chans, reqs), nil
	}
	return nil, fmt.Errorf("operator-ssh connect: %w", lastErr)
}

type opSSHStderr struct{ b *strings.Builder }

func (w *opSSHStderr) Write(p []byte) (int, error) { return w.b.Write(p) }

type opSSHConn struct {
	r         io.Reader
	w         io.WriteCloser
	closeOnce sync.Once
	closer    func()
}

func (c *opSSHConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *opSSHConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *opSSHConn) Close() error {
	c.closeOnce.Do(func() {
		if c.closer != nil {
			c.closer()
		}
	})
	return nil
}

type opSSHAddr struct{}

func (opSSHAddr) Network() string { return "vsock" }
func (opSSHAddr) String() string  { return "vsock" }

func (c *opSSHConn) LocalAddr() net.Addr                { return opSSHAddr{} }
func (c *opSSHConn) RemoteAddr() net.Addr               { return opSSHAddr{} }
func (c *opSSHConn) SetDeadline(t time.Time) error      { return nil }
func (c *opSSHConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *opSSHConn) SetWriteDeadline(t time.Time) error { return nil }

// InstanceIDByName scans the exelet's ListInstances stream and returns the
// instance ID for the given instance name. Returns "" if not found.
func InstanceIDByName(ctx context.Context, c *client.Client, name string) (string, error) {
	stream, err := c.ListInstances(ctx, &api.ListInstancesRequest{})
	if err != nil {
		return "", fmt.Errorf("ListInstances: %w", err)
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if resp.Instance != nil && resp.Instance.GetName() == name {
			return resp.Instance.GetID(), nil
		}
	}
}
