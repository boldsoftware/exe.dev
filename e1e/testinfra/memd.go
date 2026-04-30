// Memd test helpers: dial the per-VM hybrid-vsock unix socket exposed by
// cloud-hypervisor and speak the memd "GET memstat\n" → JSON+\n protocol.
//
// We share the same CONNECT-handshake plumbing as op-ssh; only the vsock
// port and request line differ.

package testinfra

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// MemdVsockPort mirrors cmd/exe-init.MemdVsockPort. Hard-coded to avoid
// importing guest-side init code.
const MemdVsockPort = 2223

// MemdSample mirrors cmd/exe-init.MemdSample. Only the fields the test
// actually inspects are listed; encoding/json ignores the rest.
type MemdSample struct {
	Version    int                    `json:"version"`
	CapturedAt time.Time              `json:"captured_at"`
	UptimeSec  float64                `json:"uptime_sec"`
	Meminfo    map[string]uint64      `json:"meminfo"`
	Vmstat     map[string]uint64      `json:"vmstat"`
	PSI        map[string]MemdPSILine `json:"psi"`
	Errors     []string               `json:"errors,omitempty"`
}

type MemdPSILine struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	Total  uint64  `json:"total"`
}

// FetchMemdSample dials the per-VM CH hybrid-vsock socket (operator-ssh
// socket; memd shares it on a different port), speaks the CONNECT
// handshake to MemdVsockPort, sends "GET memstat\n" and decodes the JSON
// reply.
func FetchMemdSample(ctx context.Context, exelet *ExeletInstance, instanceID string) (*MemdSample, error) {
	socketPath := OperatorSSHSocketPath(exelet.DataDir, instanceID)

	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cmd *exec.Cmd
	if exelet.RemoteHost == "" {
		cmd = exec.CommandContext(dialCtx, "sudo", "socat", "-t10", "-", "UNIX-CONNECT:"+socketPath)
	} else {
		cmd = exec.CommandContext(dialCtx, "ssh",
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+sshControlPath(exelet.RemoteHost),
			"-o", "ControlPersist=60s",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			exelet.RemoteHost,
			"sudo socat -t10 - UNIX-CONNECT:"+socketPath)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &opSSHStderr{b: &stderrBuf}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = stdin.Close()
			cancel()
			_ = cmd.Wait()
		})
	}
	defer cleanup()

	if _, err := fmt.Fprintf(stdin, "CONNECT %d\n", MemdVsockPort); err != nil {
		return nil, fmt.Errorf("write CONNECT: %w (stderr=%q)", err, stderrBuf.String())
	}
	br := bufio.NewReader(stdout)
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read OK: %w (stderr=%q)", err, stderrBuf.String())
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "OK") {
		return nil, fmt.Errorf("unexpected CH vsock response: %q", line)
	}
	if _, err := io.WriteString(stdin, "GET memstat\n"); err != nil {
		return nil, fmt.Errorf("write GET: %w", err)
	}
	respLine, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var s MemdSample
	if err := json.Unmarshal(respLine, &s); err != nil {
		return nil, fmt.Errorf("decode: %w (raw=%q)", err, respLine)
	}
	return &s, nil
}
