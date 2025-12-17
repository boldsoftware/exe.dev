package execore

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/container"
	"exe.dev/exemenu"
	"exe.dev/xshelley"
)

// shelleyCommand returns the command definition for the shelley command
func (ss *SSHServer) shelleyCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "shelley",
		Description: "Manage Shelley agent on VMs",
		Usage:       "shelley <subcommand> [args...]",
		Handler:     ss.handleShelleyHelp,
		Subcommands: []*exemenu.Command{
			{
				Name:              "install",
				Description:       "Install or upgrade Shelley to the current version",
				Usage:             "shelley install <vm>",
				Handler:           ss.handleShelleyInstall,
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
		},
	}
}

// handleShelleyHelp shows help for the shelley command
func (ss *SSHServer) handleShelleyHelp(ctx context.Context, cc *exemenu.CommandContext) error {
	cc.Writeln("Shelley is an agent that's pre-installed on exeuntu containers.")
	cc.Writeln("")
	cc.Writeln("Usage: shelley <subcommand>")
	cc.Writeln("")
	cc.Writeln("Available subcommands:")
	cc.Writeln("  install <vm>   Install/upgrade Shelley to the current version")
	cc.Writeln("")
	return nil
}

// handleShelleyInstall handles "shelley install <box>"
func (ss *SSHServer) handleShelleyInstall(ctx context.Context, cc *exemenu.CommandContext) error {
	// Expect exactly one argument: the box name
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: shelley install <vm>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Look up the box
	box, err := ss.server.getBoxForUserByUserID(ctx, cc.User.ID, boxName)
	if errors.Is(err, sql.ErrNoRows) || (err != nil && strings.Contains(err.Error(), "not found")) {
		return cc.Errorf("VM %q not found", boxName)
	}
	if err != nil {
		return fmt.Errorf("failed to look up VM: %w", err)
	}

	// Validate box has SSH credentials
	if box.SSHPort == nil || box.SSHUser == nil || len(box.SSHClientPrivateKey) == 0 {
		return cc.Errorf("VM %q does not have SSH configured", boxName)
	}

	// Check if this is an exeuntu-based box
	isExeuntu := strings.Contains(box.Image, "exeuntu") || box.Image == "boldsoftware/exeuntu"

	// Get the actual architecture from the exelet client
	exeletClient := ss.server.getExeletClient(box.Ctrhost)
	if exeletClient == nil {
		return fmt.Errorf("exelet host not available for VM")
	}
	arch := exeletClient.client.Arch()
	if arch == "" {
		return fmt.Errorf("architecture not available for VM host")
	}

	if !isExeuntu {
		// Not exeuntu - provide download instructions
		downloadURL := fmt.Sprintf("%s/shelley/download?arch=%s", ss.server.webBaseURLNoRequest(), arch)

		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{
				"error":        "not_exeuntu",
				"message":      "Target machine is not exeuntu-based",
				"download_url": downloadURL,
				"arch":         arch,
			})
			return nil
		}

		cc.Writeln("\033[1;33mVM %q is not exeuntu-based.\033[0m", boxName)
		cc.Writeln("")
		cc.Writeln("You can download and start shelley manually:")
		cc.Writeln("  curl -o /usr/local/bin/shelley %s", downloadURL)
		cc.Writeln("  chmod +x /usr/local/bin/shelley")
		cc.Writeln("  mkdir -p ~/.shelley")
		cc.Writeln("  nohup /usr/local/bin/shelley -db ~/.shelley/shelley.db -config /exe.dev/shelley.json serve -port 9999 &")
		cc.Writeln("")
		return nil
	}

	// Proceed with installation for exeuntu boxes
	cc.Writeln("Installing Shelley on \033[1m%s\033[0m...", boxName)

	// Get the shelley binary for the target architecture
	shelleyPath, err := xshelley.GetShelley(ctx, arch)
	if err != nil {
		return fmt.Errorf("failed to get shelley binary for %s: %w", arch, err)
	}

	// Create SSH signer
	sshSigner, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to create SSH signer: %w", err)
	}

	// Resolve SSH host
	sshHost := box.SSHHost()
	sshAddr := fmt.Sprintf("%s:%d", sshHost, *box.SSHPort)

	// Connect to the box via SSH
	sshConfig := &ssh.ClientConfig{
		User: *box.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshSigner),
		},
		HostKeyCallback: box.CreateHostKeyCallback(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to VM via SSH: %w", err)
	}
	defer client.Close()

	// Backup existing shelley if it exists
	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("/usr/local/bin/shelley.backup.%s", timestamp)

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}

	// Try to backup, but don't fail if shelley doesn't exist
	backupCmd := fmt.Sprintf("if [ -f /usr/local/bin/shelley ]; then sudo cp /usr/local/bin/shelley %s; echo 'backed_up'; else echo 'no_existing'; fi", backupPath)
	output, err := session.CombinedOutput(backupCmd)
	session.Close()

	if err == nil && strings.Contains(string(output), "backed_up") {
		cc.Writeln("Backed up existing shelley to %s", backupPath)
	}

	// SCP the shelley binary to the box using a random temp path
	tmpPath := fmt.Sprintf("/tmp/shelley.%s", crand.Text())
	if err := ss.scpFileToBox(client, shelleyPath, tmpPath); err != nil {
		return fmt.Errorf("failed to copy shelley binary: %w", err)
	}

	cc.Writeln("Copied shelley binary")

	// Move it to /usr/local/bin and make it executable
	session, err = client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	installCmd := fmt.Sprintf("sudo mv %s /usr/local/bin/shelley && sudo chmod +x /usr/local/bin/shelley", tmpPath)
	if err := session.Run(installCmd); err != nil {
		return fmt.Errorf("failed to install shelley binary: %w", err)
	}

	cc.Writeln("Installed shelley to /usr/local/bin/shelley")

	// Restart the shelley service
	session2, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session2.Close()

	restartCmd := "sudo systemctl restart shelley.service"
	if err := session2.Run(restartCmd); err != nil {
		slog.WarnContext(ctx, "Failed to restart shelley.service", "error", err)
		cc.Writeln("\033[1;33mWarning: Failed to restart shelley.service: %v\033[0m", err)
		cc.Writeln("You may need to restart it manually.")
	} else {
		cc.Writeln("Restarted shelley.service")
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name": boxName,
			"status":  "installed",
			"backup":  backupPath,
		})
		return nil
	}

	cc.Writeln("\033[1;32m✓ Shelley installation complete\033[0m")
	return nil
}

// scpFileToBox copies a file to a remote box via SCP using the golang SSH library
func (ss *SSHServer) scpFileToBox(client *ssh.Client, localPath, remotePath string) error {
	// Open the local file
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	// Get file info
	fileInfo, err := localFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	// Create a new SSH session for SCP
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Set up stdin pipe for sending file data
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	// Start the remote scp command
	remoteDir := filepath.Dir(remotePath)
	remoteFile := filepath.Base(remotePath)

	// Use -t flag for target (receive mode)
	if err := session.Start(fmt.Sprintf("scp -t %s", remoteDir)); err != nil {
		return fmt.Errorf("failed to start remote scp: %w", err)
	}

	// Send SCP protocol header: C<mode> <size> <filename>\n
	mode := fileInfo.Mode().Perm()
	header := fmt.Sprintf("C%04o %d %s\n", mode, fileInfo.Size(), remoteFile)
	if _, err := stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("failed to send SCP header: %w", err)
	}

	// Send file contents
	if _, err := io.Copy(stdin, localFile); err != nil {
		return fmt.Errorf("failed to send file contents: %w", err)
	}

	// Send end-of-file marker
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to send EOF marker: %w", err)
	}

	stdin.Close()

	// Wait for the session to complete
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp session failed: %w", err)
	}

	return nil
}
