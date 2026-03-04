package execore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"exe.dev/container"
	"exe.dev/exemenu"
	"exe.dev/exeweb"
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
			{
				Name:              "prompt",
				Description:       "Send a prompt to Shelley on a VM",
				Usage:             "shelley prompt <vm> <prompt>",
				Handler:           ss.handleShelleyPrompt,
				HasPositionalArgs: true,
				RawArgs:           true,
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
	cc.Writeln("  install <vm>          Install/upgrade Shelley to the current version")
	cc.Writeln("  prompt <vm> <prompt>  Send a prompt to Shelley on a VM")
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

	// Backup existing shelley if it exists
	timestamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("/usr/local/bin/shelley.backup.%s", timestamp)
	backupCmd := fmt.Sprintf("if [ -f /usr/local/bin/shelley ]; then sudo cp /usr/local/bin/shelley %s; echo 'backed_up'; else echo 'no_existing'; fi", backupPath)
	output, err := runCommandOnBox(ctx, ss.server.sshPool, box, backupCmd)
	if err == nil && strings.Contains(string(output), "backed_up") {
		cc.Writeln("Backed up existing shelley to %s", backupPath)
	}

	// Copy the shelley binary to the box
	shelleyFile, err := os.Open(shelleyPath)
	if err != nil {
		return fmt.Errorf("failed to open shelley binary: %w", err)
	}
	defer shelleyFile.Close()

	if err := scpToBox(ctx, ss.server.sshPool, box, shelleyFile, "/usr/local/bin/shelley", 0o755); err != nil {
		return cc.Errorf("failed to install shelley binary: %v", err)
	}
	cc.Writeln("Copied shelley binary")
	cc.Writeln("Installed shelley to /usr/local/bin/shelley")

	// Restart the shelley service
	if _, err := runCommandOnBox(ctx, ss.server.sshPool, box, "sudo systemctl restart shelley.service"); err != nil {
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

// handleShelleyPrompt handles "shelley prompt <vm> <prompt-text>"
func (ss *SSHServer) handleShelleyPrompt(ctx context.Context, cc *exemenu.CommandContext) error {
	// RawArgs: args[0] is vm name, args[1:] is the prompt text
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: shelley prompt <vm> <prompt>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
	prompt := strings.Join(cc.Args[1:], " ")

	if strings.TrimSpace(prompt) == "" {
		return cc.Errorf("prompt text is required")
	}

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

	// Create SSH signer
	sshSigner, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to create SSH signer: %w", err)
	}

	shelleyBaseURL := ss.server.shelleyURL(box.Name)

	// Non-interactive mode (web /cmd, /exec API): create conversation and return URL.
	if cc.SSHSession == nil {
		sshHost := exeweb.BoxSSHHost(ss.server.slog(), box.Ctrhost)
		httpClient := &http.Client{
			Transport: ss.server.createSSHTunnelTransport(sshHost, box, sshSigner),
			Timeout:   30 * time.Second,
		}

		conversationID, err := ss.createConversation(ctx, httpClient, prompt, cc.User.ID)
		if err != nil {
			return fmt.Errorf("failed to create Shelley conversation: %w", err)
		}

		json.NewEncoder(cc.Output).Encode(map[string]string{
			"conversation_id": conversationID,
			"shelley_url":     shelleyBaseURL + "/c/" + conversationID,
		})
		return nil
	}

	// Interactive SSH mode: stream the conversation
	model := shelleyDefaultModel
	if err := ss.runShelleyPrompt(ctx, cc, box, sshSigner, prompt, shelleyBaseURL, model); err != nil {
		cc.WriteError("Error running Shelley prompt: %v", err)
		cc.Write("Connect to Shelley at %s\r\n", shelleyBaseURL)
	}

	return nil
}

// createConversation creates a Shelley conversation without streaming.
// It POSTs to the Shelley API via the SSH tunnel and returns the conversation ID.
func (ss *SSHServer) createConversation(ctx context.Context, httpClient *http.Client, prompt, userID string) (string, error) {
	chatReq := ShelleyChatRequest{
		Message: prompt,
		Model:   "claude-sonnet-4.5",
	}
	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost:9999/api/conversations/new", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shelley-Request", "1")
	req.Header.Set("X-Exedev-Userid", userID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to connect to Shelley: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Shelley returned status %d: %s", resp.StatusCode, string(body))
	}

	var chatResp struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if chatResp.ConversationID == "" {
		return "", fmt.Errorf("no conversation ID in response")
	}

	return chatResp.ConversationID, nil
}
