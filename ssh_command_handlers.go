package exe

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"exe.dev/accounting"
	"exe.dev/billing"
	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sqlite"
	"golang.org/x/term"
)

// jsonOnlyFlags returns a FlagSet creation function for a FlagSet named name with only the --json flag.
func jsonOnlyFlags(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.Bool("json", false, "output in JSON format")
		return fs
	}
}

// newCommandFlags creates a FlagSet for the new command
func newCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.String("name", "", "box name (auto-generated if not specified)")
	fs.String("image", "exeuntu", "container image")
	fs.String("size", "medium", "box size (small, medium, or large)")
	fs.String("command", "auto", "container command: auto, none, or a custom command")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// proxyCommandFlags creates a FlagSet for the proxy command
func proxyCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.Int("port", 80, "port to expose")
	fs.Bool("private", false, "make the route private")
	fs.Bool("public", false, "make the route public")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// NewCommandTree creates a new command tree with all exe.dev commands
func NewCommandTree(ss *SSHServer) *exemenu.CommandTree {
	commands := []*exemenu.Command{
		{
			Name:              "help",
			Aliases:           []string{"?"},
			Description:       "Show help information",
			Handler:           ss.handleHelpCommand,
			HasPositionalArgs: true,
		},
		{
			Name:              "doc",
			Description:       "Browse documentation",
			Handler:           ss.handleDocCommand,
			Usage:             "doc [slug]",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeDocSlugs,
		},
		{
			Name:        "list",
			Aliases:     []string{"ls"},
			Description: "List your boxes",
			Handler:     ss.handleListCommand,
			FlagSetFunc: jsonOnlyFlags("list"),
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
			Name:              "delete",
			Description:       "Delete a box",
			Handler:           ss.handleDeleteCommand,
			FlagSetFunc:       jsonOnlyFlags("delete"),
			Usage:             "delete <box-name>",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		{
			Name:        "alloc",
			Hidden:      true,
			Description: "Resource allocation info",
			Handler:     ss.handleAllocCommand,
		},
		{
			Name:        "hireme",
			Aliases:     boxname.JobsRelated,
			Hidden:      true,
			Description: "Apply for a job at exe.dev",
			Handler:     ss.handleJobCommand,
		},
		{
			Name:        "billing",
			Description: "Manage billing and payment info",
			Handler:     ss.handleBillingCommand,
			FlagSetFunc: jsonOnlyFlags("billing"),
			Subcommands: []*exemenu.Command{
				{
					Name:        "balance",
					Description: "Get current balance of billing account",
					Usage:       "billing balance",
					Handler:     ss.handleBillingBalance,
				},
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
				{
					Name:        "fund",
					Description: "[dev mode] add credits to billing account",
					Usage:       "fund",
					Handler:     ss.handleBillingFundCommand,
				},
			},
		},
		{
			Name:              "proxy",
			Description:       "Configure box routing",
			Usage:             "proxy <box-name> [--port=80 --private|--public]",
			Handler:           ss.handleProxyCommand,
			FlagSetFunc:       proxyCommandFlags,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
			Examples: []string{
				"proxy mybox                     # show current routing config",
				"proxy mybox --port=8080 --private  # expose port 8080 privately",
				"proxy mybox --port=80 --public     # expose port 80 publicly",
				"proxy mybox --port=3000 --public   # expose port 3000 publicly",
			},
		},
		{
			Name:        "whoami",
			Description: "Show your user information including email and all SSH keys.",
			Usage:       "whoami",
			Handler:     ss.handleWhoamiCommand,
			FlagSetFunc: jsonOnlyFlags("whoami"),
		},
		{
			Name:        "browser",
			Description: "Generate a magic link to access the web UI authenticated as yourself",
			Usage:       "browser",
			Handler:     ss.handleBrowserCommand,
			FlagSetFunc: jsonOnlyFlags("browser"),
		},
		{
			Name:        "exit",
			Description: "Exit",
			Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
				fmt.Fprint(cc.Output, "Goodbye!\r\n")
				return io.EOF
			},
		},
	}

	for _, cmd := range commands {
		if err := exemenu.ValidateCommand(cmd); err != nil {
			panic(fmt.Errorf("invalid command configuration: %w", err))
		}
	}

	ct := &exemenu.CommandTree{Commands: commands}
	if ss.server != nil && ss.server.devMode == "local" {
		ct.DevMode = true
	}
	return ct
}

func (ss *SSHServer) handleHelpCommand(ctx context.Context, cc *exemenu.CommandContext) error {
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

func (ss *SSHServer) handleListCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	containers, err := ss.server.containerManager.ListContainers(ctx, cc.Alloc.ID)
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		var boxList []map[string]any
		for _, c := range containers {
			box := map[string]any{
				"box_name": c.Name,
				"status":   c.Status.String(),
			}
			imageName := container.GetDisplayImageName(c.Image)
			switch imageName {
			case "exeuntu", "":
			default:
				box["image"] = imageName
			}
			boxList = append(boxList, box)
		}
		cc.WriteJSON(map[string]any{
			"boxes": boxList,
		})
		return nil
	}

	if len(containers) == 0 {
		cc.Write("No boxes found. Create one with 'new'.\r\n")
		return nil
	}

	cc.Write("\033[1;36mYour boxes:\033[0m\r\n")
	for _, c := range containers {
		var statusColor string
		switch c.Status {
		case container.StatusRunning:
			statusColor = "\033[1;32m" // green
		case container.StatusStopped:
			statusColor = "\033[1;31m" // red
		case container.StatusPending:
			statusColor = "\033[1;33m" // yellow
		}
		cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", c.Name, statusColor, c.Status.String())
		imageName := container.GetDisplayImageName(c.Image)
		switch imageName {
		case "exeuntu", "":
		default:
			cc.Write(" (%s)", imageName)
		}
		cc.Write("\r\n")
	}
	return nil
}

func (ss *SSHServer) handleNewCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	user := cc.User
	boxName := cc.FlagSet.Lookup("name").Value.String()
	image := cc.FlagSet.Lookup("image").Value.String()
	size := cc.FlagSet.Lookup("size").Value.String()
	command := cc.FlagSet.Lookup("command").Value.String()

	// Generate box name if not provided
	if boxName == "" {
		for range 10 {
			randBoxName := boxname.Random()
			if boxname.Valid(randBoxName) && ss.server.isBoxNameAvailable(ctx, randBoxName) {
				boxName = randBoxName
				break
			}
		}
	}

	// Validate box name (both provided and generated)
	if !boxname.Valid(boxName) {
		return cc.Errorf("%s", boxname.InvalidBoxNameMessage)
	}

	if !ss.server.isBoxNameAvailable(ctx, boxName) {
		return cc.Errorf("Box name %q is not available", boxName)
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
		return cc.Errorf("Invalid container size %q. Valid sizes: micro, small, medium, large, xlarge", size)
	}

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
	boxID, err := ss.server.preCreateBox(ctx, user.ID, cc.Alloc.ID, boxName, imageToStore)
	if err != nil {
		cc.Write("\033[1;31mError: Failed to create box entry: %v\033[0m\r\n", err)
		return fmt.Errorf("failed to create box entry: %w", err)
	}

	// Create container request with progress callback
	req := &container.CreateContainerRequest{
		AllocID:         cc.Alloc.ID,
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

	// Start timing BEFORE creating container
	startTime := time.Now()

	// Determine if we should show fancy output (spinners, colors, etc) BEFORE creating container
	// Allow forced spinner (e.g., HTTP/SSE flows) via cc.ForceSpinner
	showSpinner := (ss.shouldShowSpinner(cc.SSHSession) || cc.ForceSpinner) && !cc.WantJSON()

	// Reserve space for spinner if we're showing it: print a blank line, then move cursor up.
	// This makes the readline prompt visible in the repl ui.
	if showSpinner {
		cc.Write("\r\n\033[1A")
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
		// Clean up the pre-created box entry since container creation failed
		if err := ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			queries := exedb.New(tx.Conn())
			return queries.DeleteBox(ctx, boxID)
		}); err != nil {
			slog.Error("Failed to clean up box entry after container creation failure", "boxID", boxID, "error", err)
		}
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
		ClientPrivateKey:  createdContainer.SSHClientPrivateKey,
		SSHPort:           createdContainer.SSHPort,
	}
	if err := ss.server.updateBoxWithContainer(ctx, boxID, createdContainer.ID, createdContainer.SSHUser, sshKeys, createdContainer.SSHPort); err != nil {
		return err
	}

	// Set up automatic routing based on exposed ports
	proxyPort := 80
	slog.Debug("setting up automatic routing", "box", boxName, "exposed_ports", createdContainer.ExposedPorts)
	if bestPort := container.ChooseBestPortToRoute(createdContainer.ExposedPorts); bestPort > 0 {
		var box *exedb.Box
		var err error
		if cc.PublicKey != "" {
			box, err = ss.server.getBoxForUser(ctx, cc.PublicKey, boxName)
		} else {
			// Fallback for non-SSH contexts (e.g., mobile flow) where PublicKey is empty
			box, err = ss.server.getBoxForUserByUserID(ctx, cc.User.ID, boxName)
		}
		if err != nil {
			return err
		}
		route := exedb.Route{
			Port:  bestPort,
			Share: "private",
		}
		box.SetRoute(route)
		if err := ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			queries := exedb.New(tx.Conn())
			return queries.UpdateBoxRoutes(ctx, exedb.UpdateBoxRoutesParams{
				Name:    box.Name,
				AllocID: box.AllocID,
				Routes:  box.Routes,
			})
		}); err != nil {
			slog.Warn("failed to save auto-routing setup", "box", boxName, "port", bestPort, "error", err)
		}
		proxyPort = bestPort
	}

	totalTime := time.Since(startTime)
	sshCommand := ss.server.formatSSHConnectionInfo(boxName)
	httpsProxyAddr := ss.server.httpsProxyAddress(boxName)
	if showSpinner {
		// Clear the progress line and show formatted completion message
		cc.Write("\r\033[K")
	}

	if cc.WantJSON() {
		out := map[string]any{
			"box_name":    boxName,
			"ssh_command": sshCommand,
			"https_url":   httpsProxyAddr,
			"proxy_port":  proxyPort,
		}
		cc.WriteJSON(out)
		return nil
	}
	if cc.IsInteractive() {
		cc.Write("Ready in %.1fs! Access with:\r\n\r\n\033[1m%s\033[0m\r\n\033[1m%s\033[0m (→ port %d)\r\n\r\n",
			totalTime.Seconds(), sshCommand, httpsProxyAddr, proxyPort)
	} else {
		// Non-interactive session: output clean SSH command to stdout
		cc.Write("%s\r\n%s\r\n", sshCommand, httpsProxyAddr)
	}
	return nil
}

func (ss *SSHServer) handleDeleteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("please specify exactly one box name to delete, got %d", len(cc.Args))
	}

	boxName := cc.Args[0]
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: cc.User.ID,
		})
	})
	if err != nil {
		cc.WriteError("Box %q not found", boxName)
		return nil
	}

	cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)

	// Delete the container if it exists
	if box.ContainerID != nil {
		ctx := context.Background()
		err = ss.server.containerManager.DeleteContainer(ctx, box.AllocID, *box.ContainerID)
		if err != nil {
			return err
		}
	}

	// Delete from database and track in deleted_boxes
	err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		err := queries.InsertDeletedBox(ctx, exedb.InsertDeletedBoxParams{
			ID:      int64(box.ID),
			AllocID: box.AllocID,
		})
		if err != nil {
			return fmt.Errorf("tracking deletion: %w", err)
		}
		return queries.DeleteBox(ctx, box.ID)
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		result := map[string]string{
			"box_name": boxName,
			"status":   "deleted",
		}
		cc.WriteJSON(result)
		return nil
	}
	cc.Write("\033[1;32mBox %q deleted successfully\033[0m\r\n", boxName)
	return nil
}

func (ss *SSHServer) handleAllocCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.WantJSON() {
		allocInfo := map[string]any{
			"id":      cc.Alloc.ID,
			"type":    cc.Alloc.Type,
			"region":  cc.Alloc.Region,
			"created": cc.Alloc.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
		cc.WriteJSON(allocInfo)
		return nil
	}
	cc.Writeln("\033[1;36mYour Allocation:\033[0m")
	cc.Writeln("")
	cc.Writeln("  ID: \033[1m%s\033[0m", cc.Alloc.ID)
	cc.Writeln("  Type: \033[1m%s\033[0m", cc.Alloc.Type)
	cc.Writeln("  Region: \033[1m%s\033[0m", cc.Alloc.Region)
	cc.Writeln("  Created: %s", cc.Alloc.CreatedAt.Format("Jan 2, 2006"))
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleJobCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.WantJSON() {
		jobInfo := map[string]any{
			"email": "david+repl@bold.dev",
		}
		cc.WriteJSON(jobInfo)
		return nil
	}
	cc.Writeln("")
	cc.Writeln("\033[1;36mYou found the secret careers menu item.\033[0m")
	cc.Writeln("")
	cc.Writeln("  Want to work with us? Email:")
	cc.Writeln("")
	cc.Writeln("  david+repl@bold.dev")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleBillingCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfoByAccount(ctx, cc.Alloc.BillingAccountID)
	if err != nil {
		return err
	}
	balance, err := withAccountantRxRes(ss.server, ctx, func(ctx context.Context, accountant *accounting.Accountant, rx *sqlite.Rx) (float64, error) {
		return accountant.GetBalance(ctx, rx, billingInfo.BillingAccountID)
	})
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get balance: %v\033[0m", err)
		return err
	}

	if cc.WantJSON() {
		billing := map[string]any{
			"configured":          billingInfo.HasBilling,
			"email":               billingInfo.Email,
			"stripe_customer_id":  billingInfo.StripeCustomerID,
			"current_balance_usd": balance,
		}
		cc.WriteJSON(billing)
		return nil
	}

	cc.Writeln("\033[1;36mBilling Information:\033[0m")
	cc.Writeln("")
	if billingInfo.Email != "" {
		cc.Writeln("  Email: \033[1m%s\033[0m", billingInfo.Email)
	}
	if billingInfo.StripeCustomerID != "" {
		cc.Writeln("  Stripe Customer ID: \033[1m%s\033[0m", billingInfo.StripeCustomerID)
	}
	if billingInfo.HasBilling {
		cc.Writeln("  Status: \033[1;32mConfigured\033[0m")
		cc.Writeln(". Current Balance: \033[1;32m%.2f\033[0m", balance)
	} else {
		cc.Writeln("  Status: \033[1;32mNot Configured\033[0m")
		cc.Writeln("  run `billing setup` to configure")
	}
	return nil
}

func (ss *SSHServer) handleBillingDeleteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfoByAccount(ctx, cc.Alloc.BillingAccountID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	return ss.deleteBillingInfo(cc, billingInfo)
}

func (ss *SSHServer) handleBillingFundCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfoByAccount(ctx, cc.Alloc.BillingAccountID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	credit := accounting.UsageCredit{
		BillingAccountID: billingInfo.BillingAccountID,
		Amount:           25.0,
		PaymentMethod:    "dev mode fund command",
		Status:           "completed",
	}
	err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return ss.server.accountant.CreditUsage(ctx, tx, credit)
	})
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to credit account: %v\033[0m", err)
		return err
	}
	return nil
}

func (ss *SSHServer) handleBillingUpdateEmailCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfoByAccount(ctx, cc.Alloc.BillingAccountID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	return ss.updateBillingEmail(cc, billingInfo)
}

func (ss *SSHServer) handleBillingBalance(ctx context.Context, cc *exemenu.CommandContext) error {
	billingInfo, err := ss.billing.GetBillingInfoByAccount(ctx, cc.Alloc.BillingAccountID)
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get billing info: %v\033[0m", err)
		return err
	}
	balance, err := withAccountantRxRes(ss.server, ctx, func(ctx context.Context, accountant *accounting.Accountant, rx *sqlite.Rx) (float64, error) {
		return accountant.GetBalance(ctx, rx, billingInfo.BillingAccountID)
	})
	if err != nil {
		cc.Writeln("\033[1;31mError: Failed to get balance: %v\033[0m", err)
		return err
	}

	if cc.WantJSON() {
		billing := map[string]any{
			"current_balance_usd": balance,
		}
		cc.WriteJSON(billing)
		return nil
	}
	cc.Writeln("Current balance: $%.2f", balance)
	return nil
}

func (ss *SSHServer) handleBillingSetup(ctx context.Context, cc *exemenu.CommandContext) error {
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

	err = ss.billing.SetupBilling(cc.Alloc.ID, billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		cc.Writeln("\033[1;31mError setting up billing: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing setup completed successfully!\033[0m")
	cc.Writeln("Your payment method has been verified and saved.")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleWhoamiCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	type sshKeyRow struct {
		PublicKey string `json:"public_key"`
		Current   bool   `json:"current"`
	}
	var sshKeys []sshKeyRow
	ccPubKey := strings.TrimSpace(cc.PublicKey)
	err := ss.server.db.Rx(ctx,
		func(ctx context.Context, rx *sqlite.Rx) error {
			queries := exedb.New(rx.Conn())
			publicKeys, err := queries.GetSSHKeysForUserByEmail(ctx, cc.User.Email)
			if err != nil {
				return err
			}
			for _, dbPublicKey := range publicKeys {
				dbPublicKey = strings.TrimSpace(dbPublicKey)
				if dbPublicKey == "" {
					continue
				}
				isCurrent := dbPublicKey == ccPubKey
				sshKeys = append(sshKeys, sshKeyRow{PublicKey: dbPublicKey, Current: isCurrent})
			}
			return nil
		},
	)
	if err != nil {
		return err
	}

	slices.SortFunc(sshKeys, func(a, b sshKeyRow) int {
		return cmp.Compare(a.PublicKey, b.PublicKey)
	})

	if cc.WantJSON() {
		userInfo := map[string]any{
			"email":    cc.User.Email,
			"ssh_keys": sshKeys,
		}
		cc.WriteJSON(userInfo)
		return nil
	}
	cc.Writeln("\033[1mEmail Address:\033[0m %s", cc.User.Email)
	cc.Writeln("\033[1mSSH Keys:\033[0m")
	for _, key := range sshKeys {
		cc.Write("  \033[1mPublic Key:\033[0m %s", key.PublicKey)
		if key.Current {
			cc.Write(" \033[1;32m← current\033[0m")
		}
		cc.Writeln("")
	}
	return nil
}

func (ss *SSHServer) updateBillingEmail(cc *exemenu.CommandContext, billingInfo *billing.BillingInfo) error {
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
	err = ss.billing.UpdateBillingAccountEmail(billingInfo.BillingAccountID, billingInfo.StripeCustomerID, newEmail)
	if err != nil {
		cc.Writeln("\033[1;31mError updating billing email: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing email updated successfully!\033[0m")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) deleteBillingInfo(cc *exemenu.CommandContext, billingInfo *billing.BillingInfo) error {
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
	err = ss.billing.DeleteBillingAccount(billingInfo.BillingAccountID)
	if err != nil {
		cc.Writeln("\033[1;31mError deleting billing info: %v\033[0m", err)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Billing information deleted successfully!\033[0m")
	cc.Writeln("You can set up billing again using the 'billing' command.")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleProxyCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("please specify exactly one box name to route, got %d", len(cc.Args))
	}
	boxName := cc.Args[0]

	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found", boxName)
	}
	if err != nil {
		return err
	}

	// If no flags provided (or only --json), show current configuration
	explicitFlags := map[string]bool{}
	cc.FlagSet.Visit(func(f *flag.Flag) {
		if f.Name == "json" {
			return // don't count --json as a configuration flag
		}
		explicitFlags[f.Name] = true
	})

	if len(explicitFlags) == 0 {
		route := box.GetRoute()
		if cc.WantJSON() {
			routeInfo := map[string]any{
				"box_name": boxName,
				"port":     route.Port,
				"share":    route.Share,
			}
			cc.WriteJSON(routeInfo)
			return nil
		}
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

	portSet := explicitFlags["port"]
	privateSet := explicitFlags["private"] && privateFlag != nil && privateFlag.Value.String() == "true"
	publicSet := explicitFlags["public"] && publicFlag != nil && publicFlag.Value.String() == "true"

	// Validate: if any flag is set, both --port and one of --private/--public must be set
	var flagMistake string
	if portSet || privateSet || publicSet {
		switch {
		case !portSet:
			flagMistake = "--port is required when setting proxy configuration"
		case !privateSet && !publicSet:
			flagMistake = "either --private or --public is required when setting proxy configuration"
		case privateSet && publicSet:
			flagMistake = "cannot specify both --private and --public"
		}
	}
	if flagMistake != "" {
		return cc.Errorf("%v", flagMistake)
	}

	// Parse port
	portInt, err := strconv.Atoi(portFlag.Value.String())
	if err != nil || portInt <= 0 || portInt > 65535 {
		return cc.Errorf("--port must be a valid port number (1-65535), got %q", portFlag.Value.String())
	}

	// Determine share mode
	share := "private"
	if publicSet {
		share = "public"
	}

	// Update route configuration
	newRoute := exedb.Route{
		Port:  portInt,
		Share: share,
	}

	box.SetRoute(newRoute)
	err = ss.server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpdateBoxRoutes(ctx, exedb.UpdateBoxRoutesParams{
			Routes:  box.Routes,
			Name:    boxName,
			AllocID: cc.Alloc.ID,
		})
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		result := map[string]any{
			"box_name": boxName,
			"port":     newRoute.Port,
			"share":    newRoute.Share,
			"status":   "updated",
		}
		cc.WriteJSON(result)
		return nil
	}
	cc.Writeln("\033[1;32m✓ Route updated successfully\033[0m")
	cc.Writeln("  Port: %d", newRoute.Port)
	cc.Writeln("  Share: %s", newRoute.Share)
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleBrowserCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	// Generate a verification token using the same system as email authentication.
	// The verification code for email is anti-phishing, but not needed here since the user directly acquires the link.
	token := ss.server.generateRegistrationToken()

	// Store verification in database using the existing email verification table
	err := ss.server.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertEmailVerification(ctx, exedb.InsertEmailVerificationParams{
			Token:     token,
			Email:     cc.User.Email,
			UserID:    cc.User.ID,
			ExpiresAt: time.Now().Add(15 * time.Minute), // 15 minute expiry
		})
	})
	if err != nil {
		return err
	}

	baseURL := ss.server.getBaseURL()
	magicURL := fmt.Sprintf("%s/auth/verify?token=%s", baseURL, token)
	if cc.WantJSON() {
		magicLink := map[string]string{
			"magic_link": magicURL,
		}
		cc.WriteJSON(magicLink)
		return nil
	}
	cc.Writeln("Use this link to access the exe.dev website:")
	cc.Writeln("")
	cc.Writeln("\033[1;36m%s\033[0m", magicURL)
	cc.Writeln("")
	cc.Writeln("\033[2mThis link will expire in 15 minutes.\033[0m")
	cc.Writeln("")
	return nil
}

// withAccountantRxRes executes a function with the accountant and a read transaction, returning a value
func withAccountantRxRes[T any](s *Server, ctx context.Context, fn func(context.Context, *accounting.Accountant, *sqlite.Rx) (T, error)) (T, error) {
	var result T
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		var err error
		result, err = fn(ctx, s.accountant, rx)
		return err
	})
	return result, err
}

func (ss *SSHServer) completeBoxNames(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if ss == nil || ss.server == nil || ss.server.containerManager == nil {
		return nil
	}
	if cc == nil || cc.Alloc == nil {
		return nil
	}

	ctx := context.Background()
	containers, err := ss.server.containerManager.ListContainers(ctx, cc.Alloc.ID)
	if err != nil {
		return nil
	}

	var completions []string
	prefix := compCtx.CurrentWord
	for _, container := range containers {
		if strings.HasPrefix(container.Name, prefix) {
			completions = append(completions, container.Name)
		}
	}
	return completions
}

func (ss *SSHServer) completeDocSlugs(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if ss == nil || ss.server == nil || ss.server.docs == nil {
		return nil
	}
	store := ss.server.docs.Store()
	if store == nil {
		return nil
	}
	prefix := compCtx.CurrentWord
	var completions []string
	for _, slug := range store.Slugs() {
		if strings.HasPrefix(slug, prefix) {
			completions = append(completions, slug)
		}
	}
	return completions
}
