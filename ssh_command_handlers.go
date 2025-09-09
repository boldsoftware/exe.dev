package exe

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/container"
	"exe.dev/sqlite"
	"github.com/google/uuid"
	"golang.org/x/term"
)

// newCommandFlags creates a FlagSet for the new command
func newCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.String("name", "", "box name (auto-generated if not specified)")
	fs.String("image", "exeuntu", "container image")
	fs.String("size", "medium", "box size (small, medium, or large)")
	fs.String("command", "auto", "container command: auto, none, or a custom command")
	return fs
}

// routeCommandFlags creates a FlagSet for the route command
func routeCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.Int("port", 80, "port to expose")
	fs.Bool("private", false, "make the route private")
	fs.Bool("public", false, "make the route public")
	return fs
}

// NewCommandTree creates a new command tree with all exe.dev commands
func NewCommandTree(ss *SSHServer) *CommandTree {
	return &CommandTree{
		Commands: []*Command{
			{
				Name:              "help",
				Aliases:           []string{"?"},
				Description:       "Show help information",
				Handler:           ss.handleHelpCommand,
				HasPositionalArgs: true,
			},
			{
				Name:        "list",
				Aliases:     []string{"ls"},
				Description: "List your boxes",
				Handler:     ss.handleListCommand,
				Usage:       "list",
			},
			{
				Name:        "new",
				Description: "Create a new box",
				Handler:     ss.handleNewCommand,
				FlagSetFunc: newCommandFlags,
				Examples: []string{
					"new                                # just give me a computer",
					"new --name=b --image=ubuntu:22.04  # custom image and name",
				},
			},
			{
				Name:              "start",
				Description:       "Start a stopped box",
				Handler:           ss.handleStartCommand,
				Usage:             "start <box-name>",
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
			},
			{
				Name:              "stop",
				Description:       "Stop one or more box",
				Handler:           ss.handleStopCommand,
				Usage:             "stop <box-name> [<box-name>...]",
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
			},
			{
				Name:              "delete",
				Description:       "Delete a box",
				Handler:           ss.handleDeleteCommand,
				Usage:             "delete <box-name>",
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
			},
			{
				Name:              "logs",
				Description:       "View box logs",
				Handler:           ss.handleLogsCommand,
				Usage:             "logs <box-name>",
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
			},
			{
				Name:              "diag",
				Aliases:           []string{"diagnostics"},
				Description:       "Get box startup diagnostics",
				Usage:             "diag <box-name>",
				Handler:           ss.handleDiagCommand,
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
			},
			{
				Name:        "alloc",
				Description: "Resource allocation info",
				Handler:     ss.handleAllocCommand,
				Subcommands: []*Command{
					{
						Name:        "info",
						Description: "Show allocation usage",
						Usage:       "alloc info",
						Handler:     ss.handleAllocInfoCommand,
					},
				},
			},
			{
				Name:        "billing",
				Description: "Manage billing and payment info",
				Handler:     ss.handleBillingCommand,
				Subcommands: []*Command{
					{
						Name:        "setup",
						Description: "Set up billing information",
						Usage:       "billing setup",
						Handler:     ss.handleBillingSetup,
					},
					{
						Name:        "update",
						Description: "Update billing email address",
						Usage:       "billing update",
						Handler:     ss.handleBillingUpdateEmailCommand,
					},
					{
						Name:        "delete",
						Description: "Delete billing information",
						Usage:       "billing delete",
						Handler:     ss.handleBillingDeleteCommand,
					},
				},
			},
			{
				Name:              "route",
				Description:       "Configure box routing",
				Usage:             "route <box-name> [--port=80 --private|--public]",
				Handler:           ss.handleRouteCommand,
				FlagSetFunc:       routeCommandFlags,
				HasPositionalArgs: true,
				CompleterFunc:     CompleteBoxNames,
				Examples: []string{
					"route mybox                     # show current routing config",
					"route mybox --port=8080 --private  # expose port 8080 privately",
					"route mybox --port=80 --public     # expose port 80 publicly",
					"route mybox --port=3000 --public   # expose port 3000 publicly",
				},
			},
			{
				Name:        "whoami",
				Description: "Show your user information including email and all SSH keys. The currently connected key is highlighted.",
				Usage:       "whoami",
				Handler:     ss.handleWhoamiCommand,
			},
			{
				Name:        "exit",
				Description: "Exit",
				Handler: func(ctx context.Context, cc *CommandContext) error {
					fmt.Fprint(cc.Output, "Goodbye!\r\n")

					return io.EOF
				},
			},
		},
	}
}

func (ss *SSHServer) handleHelpCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) > 0 {
		// Help for specific command
		cmdPath := cc.Args
		cmd := ss.commands.FindCommand(cmdPath)
		if cmd == nil {
			cc.Writeln("No help available for unrecognized command: %s", strings.Join(cmdPath, " "))
			return nil
		}

		cmd.Help(cc)
		return nil
	}

	// General help
	cc.Writeln("\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n")

	ss.commands.Help(cc)

	cc.Writeln("\r\nRun \033[1mhelp <command>\033[0m for more details\r\n")
	return nil
}

func (ss *SSHServer) handleListCommand(ctx context.Context, cc *CommandContext) error {
	// If container manager is available, get real-time status
	if ss.server.containerManager != nil {
		containers, err := ss.server.containerManager.ListContainers(ctx, cc.Alloc.AllocID)
		if err != nil {
			cc.Write("\033[1;31mError listing boxes: %v\033[0m\r\n", err)
			return fmt.Errorf("listing boxes: %w", err)
		}

		if len(containers) == 0 {
			cc.Write("No boxes found. Create one with 'new'.\r\n")
			return fmt.Errorf("no boxes found")
		}

		cc.Write("\033[1;36mYour boxes:\033[0m\r\n")
		for _, c := range containers {
			status := string(c.Status)
			statusColor := ""
			switch c.Status {
			case container.StatusRunning:
				statusColor = "\033[1;32m" // green
				status = "running"
			case container.StatusStopped:
				statusColor = "\033[1;31m" // red
				status = "stopped"
			case container.StatusPending:
				statusColor = "\033[1;33m" // yellow
				status = "starting"
			}

			// Show box with colored status
			cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", c.Name, statusColor, status)

			// Add image info if available
			if c.Image != "" && c.Image != "exeuntu" {
				displayImage := container.GetDisplayImageName(c.Image)
				cc.Write(" (%s)", displayImage)
			}

			cc.Write("\r\n")
		}
		return nil
	}

	// Fallback to database if container manager not available
	boxes, err := ss.server.getBoxesForAlloc(ctx, cc.Alloc.AllocID)
	if err != nil {
		cc.Write("\033[1;31mError listing boxes: %v\033[0m\r\n", err)
		return fmt.Errorf("listing boxes: %w", err)
	}

	if len(boxes) == 0 {
		cc.Write("No boxes found. Create one with 'new'.\r\n")
		return fmt.Errorf("no boxes found")
	}

	cc.Write("\033[1;36mYour boxes:\033[0m\r\n")
	for _, m := range boxes {
		status := m.Status
		statusColors := map[string]string{
			"running": "\033[1;32m",
			"stopped": "\033[1;31m",
			"pending": "\033[1;33m",
		}
		cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", m.Name, statusColors[status], status)

		// Add image info if available
		if m.Image != "" && m.Image != "exeuntu" && m.Image != "ubuntu" {
			cc.Write(" (%s)", m.Image)
		}

		cc.Write("\r\n")
	}
	return nil
}

func (ss *SSHServer) handleNewCommand(ctx context.Context, cc *CommandContext) error {
	if ss.server.containerManager == nil {
		cc.Write("\033[1;31mBox management is not available\033[0m\r\n")
		return fmt.Errorf("box management is not available")
	}

	// Get user information - needed for box creation
	user, err := ss.server.getUserByPublicKey(ctx, cc.PublicKey)
	if err != nil {
		cc.Write("\033[1;31mError: Failed to get user info: %v\033[0m\r\n", err)
		return fmt.Errorf("failed to get user info: %w", err)
	}

	// Get flag values from the already-parsed FlagSet
	var boxName, image, size, command string
	if cc.FlagSet != nil {
		boxName = cc.FlagSet.Lookup("name").Value.String()
		image = cc.FlagSet.Lookup("image").Value.String()
		size = cc.FlagSet.Lookup("size").Value.String()
		command = cc.FlagSet.Lookup("command").Value.String()
	} else {
		// Default values if no flags were parsed
		image = "exeuntu"
		size = "medium"
		command = "auto"
	}

	// Check for non-flag arguments - not supported
	if len(cc.Args) > 0 {
		cc.Write("\033[1;31mError: Unexpected arguments: %s\033[0m\r\n", strings.Join(cc.Args, " "))
		cc.Write("Usage: new [--name=<name>] [--image=<image>] [--size=<size>] [--command=<auto|none|command>]\r\n")
		return fmt.Errorf("unexpected arguments: %q", strings.Join(cc.Args, " "))
	}

	// Generate box name if not provided
	if boxName == "" {
		boxName = generateRandomContainerName()
		// Check if name is already taken
		_, err := ss.server.getBoxByName(ctx, boxName)
		if err == nil {
			// Name exists, try again
			for range 10 {
				boxName = generateRandomContainerName()
				_, err = ss.server.getBoxByName(ctx, boxName)
				if err != nil {
					break
				}
			}
		}
	}

	// Validate box name (both provided and generated)
	if !ss.server.isValidBoxName(boxName) {
		cc.Write("\033[1;31mInvalid box name %q. Box names must be at least 5 characters, lowercase, start with a letter, contain only letters, numbers and hyphens (no consecutive hyphens), not use common computer terms, and be up to 64 characters\033[0m\r\n", boxName)
		return fmt.Errorf("invalid box name %q", boxName)
	}

	if _, err := ss.server.getBoxByName(ctx, boxName); err == nil {
		cc.Write("\033[1;31mBox name %q is not available\033[0m\r\n", boxName)
		return fmt.Errorf("box name %q is not available", boxName)
	}

	// Get the display image name
	displayImage := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		displayImage = "boldsoftware/exeuntu"
	}

	// Show creation message with proper formatting
	cc.Write("Creating \033[1m%s\033[0m (%s) using image \033[1m%s\033[0m...\r\n",
		boxName, size, displayImage)

	// Get size preset
	sizePreset, exists := container.ContainerSizes[size]
	if !exists {
		cc.Write("\033[1;31mError: Invalid size '%s'. Valid sizes: micro, small, medium, large, xlarge\033[0m\r\n", size)
		return fmt.Errorf("invalid container size %q", size)
	}

	// Determine if we should show fancy output (spinners, colors, etc) BEFORE creating container
	showSpinner := ss.shouldShowSpinner(cc.SSHSession)

	// Reserve space for spinner if we're showing it: print a blank line, then move cursor up.
	// This makes the readline prompt visible in the repl ui.
	if showSpinner {
		cc.Write("\r\n\033[1A")
	}

	// Start timing BEFORE creating container
	startTime := time.Now()

	// Create channels for progress updates and completion
	type progressUpdate struct {
		info container.CreateProgressInfo
	}
	progressChan := make(chan progressUpdate, 10)
	completionChan := make(chan struct {
		container *container.Container
		err       error
	}, 1)

	// Pre-create box in database to get its ID
	imageToStore := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		imageToStore = "boldsoftware/exeuntu"
	}
	boxID, err := ss.server.preCreateBox(ctx, user.UserID, cc.Alloc.AllocID, boxName, imageToStore)
	if err != nil {
		cc.Write("\033[1;31mError: Failed to create box entry: %v\033[0m\r\n", err)
		return fmt.Errorf("failed to create box entry: %w", err)
	}

	// Create container request with progress callback
	req := &container.CreateContainerRequest{
		AllocID:         cc.Alloc.AllocID,
		Name:            boxName,
		BoxID:           boxID,
		Image:           image,
		Size:            size,
		CPURequest:      sizePreset.CPURequest,
		MemoryRequest:   sizePreset.MemoryRequest,
		StorageSize:     sizePreset.StorageSize,
		Ephemeral:       false,
		CommandOverride: command,
	}

	// Add progress callback that sends to channel
	if showSpinner {
		req.ProgressCallbackEx = func(info container.CreateProgressInfo) {
			select {
			case progressChan <- progressUpdate{info}:
			default:
				// Channel full, skip this update
			}
		}
	}

	// Run CreateContainer in a goroutine
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		createdContainer, err := ss.server.containerManager.CreateContainer(ctx, req)
		completionChan <- struct {
			container *container.Container
			err       error
		}{createdContainer, err}
	}()

	// Track current progress state
	currentStatus := "Initializing"
	var imageSize int64
	var downloadedBytes int64
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIndex := 0

	// Main UI update loop
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var createdContainer *container.Container
	var createErr error

	for {
		select {
		case update := <-progressChan:
			// Update current status based on progress
			if update.info.ImageBytes > 0 {
				imageSize = update.info.ImageBytes
			}
			downloadedBytes = update.info.DownloadedBytes

			switch update.info.Phase {
			case container.CreateInit:
				currentStatus = "Initializing"
			case container.CreatePull:
				if imageSize > 0 && downloadedBytes > 0 {
					// Show download progress in MB
					curMB := downloadedBytes / (1024 * 1024)
					totalMB := imageSize / (1024 * 1024)
					currentStatus = fmt.Sprintf("Pulling image (%d/%dMB)", curMB, totalMB)
				} else if imageSize > 0 {
					totalMB := imageSize / (1024 * 1024)
					currentStatus = fmt.Sprintf("Pulling image (0/%dMB)", totalMB)
				} else {
					currentStatus = "Pulling image"
				}
			case container.CreateStart:
				currentStatus = "Starting container"
			case container.CreateSSH:
				currentStatus = "Configuring SSH"
			case container.CreateDone:
				currentStatus = "Finalizing"
			}

		case result := <-completionChan:
			createdContainer = result.container
			createErr = result.err
			goto done

		case <-ticker.C:
			// Update spinner every 100ms
			if showSpinner {
				elapsed := time.Since(startTime)
				spinner := spinners[spinnerIndex%len(spinners)]
				spinnerIndex++
				cc.Write("\r\033[K%s %.1fs %s...", spinner, elapsed.Seconds(), currentStatus)
			}
		}
	}

done:
	// Clear the progress line
	if showSpinner {
		cc.Write("\r\033[K")
	}

	if createErr != nil {
		guid := uuid.New().String() // for x-ref on support tickets
		slog.Debug("createContainer error", "error", createErr, "publicKey", cc.PublicKey, "userID", user.UserID, "allocID", cc.Alloc.AllocID, "boxName", boxName, "image", image, "size", size, "guid", guid)

		// Clean up the pre-created box entry since container creation failed
		if err := ss.server.db.Exec(ctx, `DELETE FROM boxes WHERE id = ?`, boxID); err != nil {
			slog.Error("Failed to clean up box entry after container creation failure", "boxID", boxID, "error", err)
		}

		if ss.server.devMode != "" {
			cc.Write("\033[1;31mRaw error (dev only):\r\n%v\033[0m\r\n\r\n", createErr)
		}
		cc.Write("\033[1;31mSorry, something went wrong. Error ID: %v\033[0m\r\n", guid)
		return createErr
	}

	// Update box with container info and SSH keys
	if createdContainer.SSHServerIdentityKey == "" {
		cc.Write("\033[1;31mError: Container created without SSH keys - this should not happen\033[0m\r\n")
		return fmt.Errorf("container created without SSH keys - this should not happen")
	}

	// Container has SSH keys, update the box entry
	sshKeys := &container.ContainerSSHKeys{
		ServerIdentityKey: createdContainer.SSHServerIdentityKey,
		AuthorizedKeys:    createdContainer.SSHAuthorizedKeys,
		CAPublicKey:       createdContainer.SSHCAPublicKey,
		HostCertificate:   createdContainer.SSHHostCertificate,
		ClientPrivateKey:  createdContainer.SSHClientPrivateKey,
		SSHPort:           createdContainer.SSHPort,
	}
	if err := ss.server.updateBoxWithContainer(ctx, boxID, createdContainer.ID, createdContainer.SSHUser, sshKeys, createdContainer.SSHPort); err != nil {
		cc.Write("\033[1;33mWarning: Failed to update box info: %v\033[0m\r\n", err)
	}

	// Container is ready with SSH already configured!
	// CreateContainer now blocks until SSH is verified, so we can proceed immediately

	totalTime := time.Since(startTime)
	sshCommand := ss.server.formatSSHConnectionInfo(cc.Alloc.AllocID, boxName)
	if showSpinner {
		// Clear the progress line and show formatted completion message
		cc.Write("\r\033[K")
		cc.Write("Ready in %.1fs! Access with:\r\n\r\n\033[1m%s\033[0m\r\n\r\n",
			totalTime.Seconds(), sshCommand)
	} else {
		// Non-interactive session: output clean SSH command to stdout
		cc.Write("%s\r\n", sshCommand)
	}
	return nil
}

func (ss *SSHServer) handleStartCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) == 0 {
		cc.Writeln("\033[1;31mError: Please specify a box name\033[0m")
		cc.Writeln("Usage: start <box-name>")
		return nil
	}

	boxName := cc.Args[0]

	if ss.server.containerManager == nil {
		cc.Writeln("\033[1;31mMachine management is not available\033[0m")
		return nil
	}

	// Get box info
	box, err := ss.server.getBoxByName(ctx, boxName)
	if err != nil {
		cc.Writeln("\033[1;31mError: Box '%s' not found\033[0m", boxName)
		return nil
	}

	if box.ContainerID == nil {
		cc.Writeln("\033[1;31mError: Box '%s' has no container ID\033[0m", boxName)
		return nil
	}

	cc.Writeln("Starting \033[1m%s\033[0m...", boxName)

	// Start the container
	err = ss.server.containerManager.StartContainer(ctx, box.AllocID, *box.ContainerID)
	if err != nil {
		cc.Writeln("\033[1;31mError starting box: %v\033[0m", err)
		return nil
	}

	// Update database status
	err = ss.server.db.Exec(ctx, `
		UPDATE boxes SET status = 'running', last_started_at = CURRENT_TIMESTAMP
		WHERE name = ?`,
		boxName)
	if err != nil {
		cc.Writeln("\033[1;33mWarning: Failed to update box status: %v\033[0m", err)
	}

	sshCommand := ss.server.formatSSHConnectionInfo(cc.Alloc.AllocID, boxName)
	cc.Writeln("\033[1;32mBox started!\033[0m Access with:\r\n\r\n\033[1m%s\033[0m\r\n", sshCommand)
	return nil
}

func (ss *SSHServer) handleStopCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) == 0 {
		cc.Writeln("\033[1;31mError: Please specify at least one machine name\033[0m")
		cc.Writeln("Usage: stop <machine-name> [...]")
		return nil
	}

	if ss.server.containerManager == nil {
		cc.Writeln("\033[1;31mMachine management is not available\033[0m")
		return nil
	}

	for _, boxName := range cc.Args {
		// Get box info
		box, err := ss.server.getBoxByName(ctx, boxName)
		if err != nil {
			cc.Writeln("\033[1;31mError: Box '%s' not found\033[0m", boxName)
			continue
		}

		if box.ContainerID == nil {
			cc.Writeln("\033[1;31mError: Box '%s' has no container ID\033[0m", boxName)
			continue
		}

		cc.Writeln("Stopping \033[1m%s\033[0m...", boxName)

		// Stop the container
		ctx := context.Background()
		err = ss.server.containerManager.StopContainer(ctx, box.AllocID, *box.ContainerID)
		if err != nil {
			cc.Writeln("\033[1;31mError stopping box %s: %v\033[0m", boxName, err)
			continue
		}

		// Update database status
		err = ss.server.db.Exec(ctx, `
			UPDATE boxes SET status = 'stopped'
			WHERE name = ?`,
			boxName)
		if err != nil {
			cc.Writeln("\033[1;33mWarning: Failed to update box status: %v\033[0m", err)
			return fmt.Errorf("failed to update box status: %w", err)
		}

		cc.Writeln("\033[1;32mBox '%s' stopped\033[0m", boxName)
	}
	return nil
}

func (ss *SSHServer) handleDeleteCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) == 0 {
		cc.Writeln("\033[1;31mError: Please specify a box name\033[0m")
		cc.Writeln("Usage: delete <box-name>")
		return nil
	}

	boxName := cc.Args[0]

	if ss.server.containerManager == nil {
		// Just delete from database if no container manager
		cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)

		// Get box info before deleting to track it
		var boxID int
		var allocID string
		err := ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			// First get the box info
			err := tx.QueryRow(`SELECT id, alloc_id FROM boxes WHERE name = ?`, boxName).Scan(&boxID, &allocID)
			if err != nil {
				return err
			}

			// Track deletion in deleted_boxes table
			_, err = tx.Exec(`INSERT INTO deleted_boxes (id, alloc_id) VALUES (?, ?)`, boxID, allocID)
			if err != nil {
				return fmt.Errorf("tracking deletion: %w", err)
			}

			// Delete the box
			_, err = tx.Exec(`DELETE FROM boxes WHERE id = ?`, boxID)
			return err
		})
		if err != nil {
			cc.Writeln("\033[1;31mError deleting box: %v\033[0m", err)
			return fmt.Errorf("deleting box: %w", err)
		}

		cc.Writeln("\033[1;32mBox '%s' deleted\033[0m", boxName)
		return nil
	}

	// Get box info
	box, err := ss.server.getBoxByName(ctx, boxName)
	if err != nil {
		cc.Writeln("\033[1;31mError: Box '%s' not found\033[0m", boxName)
		return fmt.Errorf("box %q not found: %w", boxName, err)
	}

	cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)

	// Delete the container if it exists
	if box.ContainerID != nil {
		ctx := context.Background()
		err = ss.server.containerManager.DeleteContainer(ctx, box.AllocID, *box.ContainerID)
		if err != nil {
			cc.Writeln("\033[1;33mWarning: Failed to delete container: %v\033[0m", err)
		}
	}

	// Delete from database and track in deleted_boxes
	err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		// Track deletion in deleted_boxes table
		_, err := tx.Exec(`INSERT INTO deleted_boxes (id, alloc_id) VALUES (?, ?)`, box.ID, box.AllocID)
		if err != nil {
			return fmt.Errorf("tracking deletion: %w", err)
		}

		// Delete the box
		_, err = tx.Exec(`DELETE FROM boxes WHERE id = ?`, box.ID)
		return err
	})
	if err != nil {
		cc.Writeln("\033[1;31mError deleting box from database: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32mBox '%s' deleted successfully\033[0m", boxName)
	return nil
}

func (ss *SSHServer) handleLogsCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) == 0 {
		cc.Writeln("\033[1;31mError: Please specify a box name\033[0m")
		cc.Writeln("Usage: logs <box-name>")
		return nil
	}

	boxName := cc.Args[0]
	cc.Writeln("Fetching logs for box '%s'...", boxName)
	cc.Writeln("\033[1;33mNote: Logs not implemented in new server yet\033[0m")
	return nil
}

func (ss *SSHServer) handleDiagCommand(ctx context.Context, cc *CommandContext) error {
	cc.Writeln("\033[1;33mDiagnostics not implemented in new server yet\033[0m")
	return nil
}

func (ss *SSHServer) handleAllocCommand(ctx context.Context, cc *CommandContext) error {
	// If no subcommand, show alloc info
	if len(cc.Args) == 0 {
		return ss.handleAllocInfoCommand(ctx, cc)
	}
	return fmt.Errorf("alloc subcommand not found: %s", strings.Join(cc.Args, " "))
}

func (ss *SSHServer) handleAllocInfoCommand(ctx context.Context, cc *CommandContext) error {
	// Show allocation info
	user, err := ss.server.getUserByPublicKey(ctx, cc.PublicKey)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get user info: %v\033[0m", err)
		return nil
	}

	alloc, err := ss.server.getUserAlloc(ctx, user.UserID)
	if err != nil || alloc == nil {
		cc.Writeln("\033[1;31mError: No allocation found\033[0m")
		return nil
	}

	cc.Writeln("\033[1;36mYour Allocation:\033[0m")
	cc.Writeln("")
	cc.Writeln("  ID: \033[1m%s\033[0m", alloc.AllocID)
	cc.Writeln("  Type: \033[1m%s\033[0m", alloc.AllocType)
	cc.Writeln("  Region: \033[1m%s\033[0m", alloc.Region)
	cc.Writeln("  Created: %s", alloc.CreatedAt.Format("Jan 2, 2006"))
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleBillingCommand(ctx context.Context, cc *CommandContext) error {
	// Get billing info to determine if user has billing set up
	billingInfo, err := ss.billing.GetBillingInfo(ctx, cc.Alloc.AllocID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	cc.Writeln("\033[1;36mBilling Information:\033[0m")
	cc.Writeln("")

	// Show current billing info
	if billingInfo.Email != "" {
		cc.Writeln("  Email: \033[1m%s\033[0m", billingInfo.Email)
	}
	if billingInfo.StripeCustomerID != "" {
		cc.Writeln("  Stripe Customer ID: \033[1m%s\033[0m", billingInfo.StripeCustomerID)
	}
	if billingInfo.HasBilling {
		cc.Writeln("  Status: \033[1;32mConfigured\033[0m")
	} else {
		cc.Writeln("  Status: \033[1;32mNot Configured\033[0m")
		cc.Writeln("  run `billing setup` to configure")
	}
	return nil
}

func (ss *SSHServer) handleBillingDeleteCommand(ctx context.Context, cc *CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfo(ctx, cc.Alloc.AllocID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	return ss.deleteBillingInfo(cc, billingInfo)
}

func (ss *SSHServer) handleBillingUpdateEmailCommand(ctx context.Context, cc *CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfo(ctx, cc.Alloc.AllocID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	return ss.updateBillingEmail(cc, billingInfo)
}

func (ss *SSHServer) handleBillingSetup(ctx context.Context, cc *CommandContext) error {
	cc.Writeln("\033[1;33mBilling Setup\033[0m")
	cc.Writeln("")
	cc.Writeln("You need to set up billing information to continue using exe.dev.")
	cc.Writeln("")

	// Requires ssh.Session for terminal interaction
	if cc.SSHSession == nil {
		cc.Writeln("\033[1;31mError: Interactive billing setup requires SSH session\033[0m")
		return fmt.Errorf("interactive billing setup requires SSH session")
	}

	terminal := term.NewTerminal(cc.SSHSession, "")

	// Get billing email
	terminal.SetPrompt("Billing email (press Enter to use " + cc.User.Email + "): ")
	billingEmail, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Billing setup cancelled.")
		return nil
	}

	if strings.TrimSpace(billingEmail) == "" {
		billingEmail = cc.User.Email
	}

	// Validate email
	if !ss.billing.ValidateEmail(billingEmail) {
		cc.Writeln("\033[1;31mInvalid email format.\033[0m")
		return nil
	}

	// Get credit card info
	cc.Writeln("Now we need to verify your payment method.")
	cc.Writeln("Please enter a credit card number to verify your payment method.")
	cc.Writeln("For testing, you can use: \033[1m4242424242424242\033[0m (Visa test card)")
	cc.Writeln("")

	terminal.SetPrompt("Credit card number: ")
	cardNumber, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Billing setup cancelled.")
		return nil
	}

	cardNumber = strings.ReplaceAll(strings.TrimSpace(cardNumber), " ", "")
	if len(cardNumber) < 13 {
		cc.Writeln("\033[1;31mInvalid card number.\033[0m")
		return nil
	}

	// Get expiry month
	terminal.SetPrompt("Expiry month (MM): ")
	expMonth, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Billing setup cancelled.")
		return nil
	}

	// Get expiry year
	terminal.SetPrompt("Expiry year (YYYY): ")
	expYear, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Billing setup cancelled.")
		return nil
	}

	// Get CVC
	terminal.SetPrompt("CVC: ")
	cvc, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Billing setup cancelled.")
		return nil
	}

	// Setup billing using the billing service
	cc.Writeln("Processing payment method...")

	err = ss.billing.SetupBilling(cc.Alloc.AllocID, billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		cc.Writeln("\033[1;31mError setting up billing: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing setup completed successfully!\033[0m")
	cc.Writeln("Your payment method has been verified and saved.")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleWhoamiCommand(ctx context.Context, cc *CommandContext) error {
	cc.Writeln("\033[1;36mUser Information:\033[0m")
	cc.Writeln("")
	cc.Writeln("\033[1mEmail Address:\033[0m %s", cc.User.Email)

	// Get the current session's public key
	currentPublicKey := ""
	if cc.SSHSession != nil {
		if key, ok := cc.SSHSession.Context().Value("public_key").(string); ok {
			currentPublicKey = key
		}
	}

	keyCount := 0

	// Get all public keys for this user
	err := ss.server.db.Rx(ctx,
		func(ctx context.Context, rx *sqlite.Rx) error {
			rows, err := rx.Query(
				`SELECT public_key FROM ssh_keys WHERE user_id = (SELECT user_id FROM users WHERE email = ?) ORDER BY public_key`,
				cc.User.Email)
			if err != nil {
				cc.Writeln("\033[1;31mError retrieving SSH keys: %v\033[0m", err)
				return nil
			}
			defer rows.Close()
			cc.Writeln("\033[1mSSH Keys:\033[0m")
			for rows.Next() {
				var dbPublicKey string
				if err := rows.Scan(&dbPublicKey); err != nil {
					continue
				}
				keyCount++

				// Check if this is the current key being used
				isCurrent := strings.TrimSpace(dbPublicKey) == strings.TrimSpace(currentPublicKey)
				currentIndicator := ""
				if isCurrent {
					currentIndicator = " \033[1;32m← current\033[0m"
				}

				// Use the current session's key if this is the current key, otherwise use DB key
				displayKey := dbPublicKey
				if isCurrent && currentPublicKey != "" {
					displayKey = currentPublicKey
				}

				if displayKey != "" {
					cc.Writeln("  \033[1mPublic Key:\033[0m %s%s", strings.TrimSpace(displayKey), currentIndicator)
				} else {
					cc.Writeln("  \033[1mPublic Key:\033[0m \033[2m(not available)\033[0m%s", currentIndicator)
				}
				cc.Writeln("")
			}

			return nil
		})
	if err != nil {
		cc.Writeln("\033[1;31mError retrieving SSH keys: %v\033[0m", err)
		return nil
	}

	if keyCount == 0 {
		cc.Writeln("  \033[2mNo SSH keys found\033[0m")
	}

	cc.Writeln("")
	return nil
}

func (ss *SSHServer) updateBillingEmail(cc *CommandContext, billingInfo *billing.BillingInfo) error {
	cc.Writeln("\033[1;33mUpdate Billing Email\033[0m")
	cc.Writeln("")

	if cc.SSHSession == nil {
		cc.Writeln("\033[1;31mError: Interactive billing email update requires SSH session\033[0m")
		return fmt.Errorf("interactive billing email update requires SSH session")
	}
	terminal := term.NewTerminal(cc.SSHSession, "")
	terminal.SetPrompt("New billing email: ")

	newEmail, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Update cancelled.")
		return nil
	}

	newEmail = strings.TrimSpace(newEmail)
	if !ss.billing.ValidateEmail(newEmail) {
		cc.Writeln("\033[1;31mInvalid email format.\033[0m")
		return nil
	}

	cc.Writeln("Updating billing email...")

	// Update billing email using billing service
	err = ss.billing.UpdateBillingEmail(billingInfo.AllocID, billingInfo.StripeCustomerID, newEmail)
	if err != nil {
		cc.Writeln("\033[1;31mError updating billing email: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing email updated successfully!\033[0m")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) deleteBillingInfo(cc *CommandContext, billingInfo *billing.BillingInfo) error {
	cc.Writeln("\033[1;33mDelete Billing Information\033[0m")
	cc.Writeln("")
	cc.Writeln("\033[1;31mWarning: This will remove all billing information from your account.\033[0m")
	cc.Writeln("You will need to set up billing again to continue using exe.dev.")
	cc.Writeln("")

	if cc.SSHSession == nil {
		cc.Writeln("\033[1;31mError: Interactive billing deletion requires SSH session\033[0m")
		return fmt.Errorf("interactive billing deletion requires SSH session")

	}
	terminal := term.NewTerminal(cc.SSHSession, "")
	terminal.SetPrompt("Are you sure? Type 'yes' to confirm: ")

	confirmation, err := terminal.ReadLine()
	if err != nil {
		cc.Writeln("Operation cancelled.")
		return nil
	}

	if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
		cc.Writeln("Operation cancelled.")
		return nil
	}

	cc.Writeln("Deleting billing information...")

	// Delete billing info using billing service
	err = ss.billing.DeleteBillingInfo(billingInfo.AllocID)
	if err != nil {
		cc.Writeln("\033[1;31mError deleting billing info: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing information deleted successfully!\033[0m")
	cc.Writeln("You can set up billing again using the 'billing' command.")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleRouteCommand(ctx context.Context, cc *CommandContext) error {
	if len(cc.Args) == 0 {
		cc.Writeln("\033[1;31mError: Please specify box name\033[0m")
		cc.Writeln("Usage: route <box-name> [--port=80 --private|--public]")
		return fmt.Errorf("box name required")
	}

	boxName := cc.Args[0]

	// Get box
	box, err := ss.server.getBoxForUser(ctx, cc.PublicKey, boxName)
	if err != nil {
		cc.Writeln("\033[1;31mError: %v\033[0m", err)
		return err
	}

	// If no flags provided, show current configuration
	if cc.FlagSet.NFlag() == 0 {
		route := box.GetRoute()
		cc.Writeln("")
		cc.Writeln("\033[1;36mRoute configuration for box '%s':\033[0m", boxName)
		cc.Writeln("  Port: %d", route.Port)
		cc.Writeln("  Share: %s", route.Share)
		cc.Writeln("")
		return nil
	}

	// Parse flags
	portFlag := cc.FlagSet.Lookup("port")
	privateFlag := cc.FlagSet.Lookup("private")
	publicFlag := cc.FlagSet.Lookup("public")

	// Determine which flags were explicitly set by the user
	setFlags := map[string]bool{}
	cc.FlagSet.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	portSet := setFlags["port"]
	privateSet := setFlags["private"] && privateFlag != nil && privateFlag.Value.String() == "true"
	publicSet := setFlags["public"] && publicFlag != nil && publicFlag.Value.String() == "true"

	// Validate: if any flag is set, both --port and one of --private/--public must be set
	if portSet || privateSet || publicSet {
		if !portSet {
			cc.Writeln("\033[1;31mError: --port is required when setting route configuration\033[0m")
			return fmt.Errorf("--port is required")
		}
		if !privateSet && !publicSet {
			cc.Writeln("\033[1;31mError: either --private or --public is required when setting route configuration\033[0m")
			return fmt.Errorf("--private or --public is required")
		}
		if privateSet && publicSet {
			cc.Writeln("\033[1;31mError: cannot specify both --private and --public\033[0m")
			return fmt.Errorf("cannot specify both --private and --public")
		}
	}

	// Parse port
	portInt, err := strconv.Atoi(portFlag.Value.String())
	if err != nil || portInt <= 0 || portInt > 65535 {
		cc.Writeln("\033[1;31mError: --port must be a valid port number (1-65535)\033[0m")
		return fmt.Errorf("invalid port value: %s", portFlag.Value.String())
	}

	// Determine share mode
	var share string
	if publicSet {
		share = "public"
	} else {
		share = "private"
	}

	// Update route configuration
	newRoute := Route{
		Port:  portInt,
		Share: share,
	}

	err = box.SetRoute(newRoute)
	if err != nil {
		cc.Writeln("\033[1;31mError encoding route: %v\033[0m", err)
		return err
	}

	// Update database
	err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET routes = ? WHERE name = ? AND alloc_id = ?`,
			*box.Routes, boxName, cc.Alloc.AllocID)
		return err
	})
	if err != nil {
		cc.Writeln("\033[1;31mError saving route: %v\033[0m", err)
		return err
	}

	cc.Writeln("\033[1;32m✓ Route updated successfully\033[0m")
	cc.Writeln("  Port: %d", newRoute.Port)
	cc.Writeln("  Share: %s", newRoute.Share)
	cc.Writeln("")
	return nil
}
