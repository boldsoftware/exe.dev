package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// handleOperatorSSHCommand prints (but does not run) the shell command an
// operator should use to SSH into a VM via the in-guest operator-SSH server
// running on AF_VSOCK. The vsock socket is exposed on the exelet host as a
// per-instance unix socket; we tunnel through the exelet host's sshd to reach
// it.
//
// Requirements:
//   - caller is in the exe sudoers list (RequiresSudo handles the gate).
//   - VM owner has run `grant-support-root <vm> on` (support_access_allowed=1).
//
// We never run anything ourselves; we just emit the command line.
func (ss *SSHServer) handleOperatorSSHCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: sudo-exe operator-ssh <vmname>")
	}
	vmName := ss.normalizeBoxName(cc.Args[0])

	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxNamed, vmName)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found", vmName)
	}
	if err != nil {
		return cc.Errorf("failed to look up VM: %v", err)
	}

	if box.SupportAccessAllowed != 1 {
		return cc.Errorf("VM %q has not granted support root access. Owner must run `grant-support-root %s on`.", vmName, vmName)
	}
	if box.ContainerID == nil || *box.ContainerID == "" {
		return cc.Errorf("VM %q has no container ID", vmName)
	}

	exeletHost := exeletHostFromCtrhost(box.Ctrhost)
	if exeletHost == "" {
		return cc.Errorf("VM %q has no usable exelet host (ctrhost=%q)", vmName, box.Ctrhost)
	}
	instanceID := *box.ContainerID

	// Per-instance vsock unix socket exposed by cloud-hypervisor on the
	// exelet host. Mirrors exelet/vmm/cloudhypervisor.(*VMM).OperatorSSHSocketPath.
	// data-dir matches the prod default in ops/deploy/exelet-prod.service;
	// if the exelet uses a different --data-dir the operator must adjust.
	const (
		vsockPort = 2222 // mirrors cmd/exe-init.OperatorSSHVsockPort
		dataDir   = "/data/exelet"
	)
	socketPath := fmt.Sprintf("%s/runtime/%s/opssh.sock", dataDir, instanceID)

	sshCmd := buildOperatorSSHCommand(exeletHost, socketPath, vsockPort)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name":     vmName,
			"exelet_host": exeletHost,
			"instance_id": instanceID,
			"socket_path": socketPath,
			"vsock_port":  vsockPort,
			"data_dir":    dataDir,
			"ssh_command": sshCmd,
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1mOperator SSH for VM %s\033[0m", vmName)
	cc.Writeln("  exelet host : %s", exeletHost)
	cc.Writeln("  instance ID : %s", instanceID)
	cc.Writeln("  vsock socket: %s (port %d)", socketPath, vsockPort)
	cc.Writeln("")
	cc.Writeln("Run from a host that can ssh to the exelet:")
	cc.Writeln("")
	cc.Writeln("  %s", sshCmd)
	cc.Writeln("")
	cc.Writeln("\033[2mAuthenticates with the same key the in-guest sshd accepts. "+
		"If the exelet uses a non-default --data-dir, replace %s in the path above.\033[0m", dataDir)
	cc.Writeln("")
	return nil
}

// buildOperatorSSHCommand builds the copy-pasteable shell command that opens
// an SSH session into the in-guest operator-SSH server running on AF_VSOCK.
//
// The ProxyCommand starts a bash coproc that runs `ssh <host> sudo socat ...`
// (bridging stdio to the per-VM unix socket on the exelet host), writes the
// `CONNECT <port>\n` line cloud-hypervisor's hybrid-vsock expects, reads and
// discards the `OK <peerport>\n` reply, and then bidirectionally bridges the
// remaining bytes between its own stdio and the coproc.
//
// Quoting layers, from outermost to innermost:
//
//	operator's shell  -- single-quoted -o argument keeps everything literal
//	/bin/sh -c        -- expands the double-quoted bash -c argument; we use
//	                     \" and \$ so they survive into bash
//	bash -c           -- the actual script; uses 'CONNECT 2222\n' and ${P[i]}
func buildOperatorSSHCommand(host, socketPath string, vsockPort int) string {
	remote := fmt.Sprintf(`sudo socat -t30 - UNIX-CONNECT:%s`, socketPath)
	// Bash script (post /bin/sh expansion). We use `echo` rather than
	// `printf 'CONNECT %d\n'` so we don't need any literal single quotes
	// inside the script -- the outer ProxyCommand= argument is itself wrapped
	// in single quotes by the operator's shell, and nesting single quotes
	// would terminate that string.
	bashScript := fmt.Sprintf(
		`coproc P { ssh %s "%s"; }; echo CONNECT %d >&${P[1]}; IFS= read -r _ <&${P[0]}; cat <&${P[0]} & exec cat >&${P[1]}`,
		host, remote, vsockPort,
	)
	// Escape for /bin/sh's double-quoted expansion: " -> \", $ -> \$.
	// (Backslashes are not in our generated string.)
	shArg := strings.NewReplacer(`"`, `\"`, `$`, `\$`).Replace(bashScript)
	proxy := `bash -c "` + shArg + `"`
	return fmt.Sprintf(
		`ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o 'ProxyCommand=%s' root@vsock`,
		proxy,
	)
}

// exeletHostFromCtrhost extracts the bare host (no port) from a ctrhost value
// like "tcp://exelet-01:9080" or "exelet-01:9080".
func exeletHostFromCtrhost(ctrhost string) string {
	if u, err := url.Parse(ctrhost); err == nil && u.Host != "" {
		host := u.Host
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		return host
	}
	host := ctrhost
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}
