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

	keyQuery := fmt.Sprintf(`sqlite3 /data/execore/exe.db "SELECT ssh_client_private_key FROM boxes WHERE name='%s'" > /tmp/opssh-key && chmod 600 /tmp/opssh-key`, vmName)
	sshCmd := buildOperatorSSHCommand(socketPath, vsockPort)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name":     vmName,
			"exelet_host": exeletHost,
			"instance_id": instanceID,
			"socket_path": socketPath,
			"vsock_port":  vsockPort,
			"data_dir":    dataDir,
			"key_query":   keyQuery,
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
	cc.Writeln("\033[1mStep 1:\033[0m Extract the VM's SSH private key (run on execore host):")
	cc.Writeln("")
	cc.Writeln("  %s", keyQuery)
	cc.Writeln("")
	cc.Writeln("\033[1mStep 2:\033[0m SSH to the exelet host:")
	cc.Writeln("")
	cc.Writeln("  ssh %s", exeletHost)
	cc.Writeln("")
	cc.Writeln("\033[1mStep 3:\033[0m From the exelet, SSH into the VM:")
	cc.Writeln("")
	cc.Writeln("  %s", sshCmd)
	cc.Writeln("")
	cc.Writeln("\033[2mIf the exelet uses a non-default --data-dir, replace %s in the socket path above.\033[0m", dataDir)
	cc.Writeln("")
	return nil
}

// buildOperatorSSHCommand builds the copy-pasteable shell command that opens
// an SSH session into the in-guest operator-SSH server running on AF_VSOCK.
// The command is meant to be run from the exelet host itself, removing one
// layer of SSH tunneling and the associated quoting complexity.
//
// The ProxyCommand uses a simple sh pipeline:
//   - { printf "CONNECT <port>\n"; cat; } sends the hybrid-vsock handshake
//     then forwards stdin into the socat tunnel
//   - sudo socat bridges stdio to the per-VM unix socket
//   - { read _; cat; } strips the "OK" response and forwards the rest
func buildOperatorSSHCommand(socketPath string, vsockPort int) string {
	// The ProxyCommand is wrapped in single quotes at the -o level (so the
	// operator's shell keeps it literal), and the inner sh -c argument uses
	// double quotes. We use `echo` instead of `printf` to avoid any nested
	// quote characters.
	proxy := fmt.Sprintf(
		`sh -c "{ echo CONNECT %d; cat; } | sudo socat -t30 - UNIX-CONNECT:%s | { read _; cat; }"`,
		vsockPort, socketPath,
	)
	return fmt.Sprintf(
		`ssh -i /tmp/opssh-key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o 'ProxyCommand=%s' root@vsock`,
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
