package execore

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/sqlite"
)

// TODO(philip): Probably can be done in Shelley itself as part of the system prompt.
const shelleyPreamble = `
The user has just created this box, and wants to do the following with it.
`

const shelleyDefaultModel = "claude-opus-4.5"

// repeatedStringFlag is a flag.Value implementation that allows a flag to be specified multiple times
type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

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
	fs.String("command", "auto", "container command: auto, none, or a custom command")
	fs.String("prompt", "", "initial prompt to send to Shelley after box creation (requires exeuntu image)")
	fs.Bool("json", false, "output in JSON format")
	fs.Bool("no-email", false, "do not send email notification")
	fs.String("prompt-model", shelleyDefaultModel, "[hidden] override the prompt model") // for testing
	fs.Bool("no-shard", false, "[hidden] skip shard allocation")
	// Environment variables (can be specified multiple times)
	var envVars repeatedStringFlag
	fs.Var(&envVars, "env", "environment variable in KEY=VALUE format (can be specified multiple times)")
	return fs
}

// NewCommandTree creates a new command tree with all exe.dev commands
func NewCommandTree(ss *SSHServer) *exemenu.CommandTree {
	commands := []*exemenu.Command{
		{
			Name:              "help",
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
			Name:        "ls",
			Description: "List your vms",
			Handler:     ss.handleListCommand,
			FlagSetFunc: jsonOnlyFlags("ls"),
			Usage:       "ls",
		},
		{
			Name:        "new",
			Description: "Create a new box",
			Handler:     ss.handleNewCommand,
			FlagSetFunc: newCommandFlags,
			Examples: []string{
				"new                                     # just give me a computer",
				"new --name=b --image=ubuntu:22.04       # custom image and name",
				"new --env FOO=bar --env BAZ=qux         # with environment variables",
			},
		},
		{
			Name:              "rm",
			Description:       "Delete a box",
			Handler:           ss.handleDeleteCommand,
			FlagSetFunc:       jsonOnlyFlags("rm"),
			Usage:             "rm <box-name>",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		{
			Name:        "hireme",
			Aliases:     boxname.JobsRelated,
			Hidden:      true,
			Description: "Apply for a job at exe.dev",
			Handler:     ss.handleJobCommand,
		},
		{
			Name:              "proxy-token",
			Hidden:            true,
			Description:       "Generate a proxy bearer token",
			FlagSetFunc:       jsonOnlyFlags("proxy-token"),
			Handler:           ss.handleProxyTokenCommand,
			Usage:             "proxy-token <box-name>",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		ss.shareCommand(),
		{
			Name:        "whoami",
			Description: "Show your user information including email and all SSH keys.",
			Usage:       "whoami",
			Handler:     ss.handleWhoamiCommand,
			FlagSetFunc: jsonOnlyFlags("whoami"),
		},
		{
			Name:              "delete-ssh-key",
			Description:       "Delete an SSH key",
			Usage:             "delete-ssh-key <public-key>",
			Handler:           ss.handleDeleteSSHKeyCommand,
			FlagSetFunc:       jsonOnlyFlags("delete-ssh-key"),
			HasPositionalArgs: true,
		},
		ss.shelleyCommand(),
		{
			Name:              "ssh",
			Description:       "SSH into a box",
			Usage:             "ssh <box-name> [command...]",
			Handler:           ss.handleSSHCommand,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		{
			Name:        "browser",
			Description: "Generate a magic link to log in to the website",
			Usage:       "browser",
			Handler:     ss.handleBrowserCommand,
			FlagSetFunc: jsonOnlyFlags("browser"),
		},
		{
			Name:        "clear",
			Description: "Clear the screen",
			Hidden:      true, // people who want this will find it; no need to clutter help
			Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
				// ANSI escape sequence to clear screen and move cursor home
				fmt.Fprint(cc.Output, "\033[2J\033[H")
				return nil
			},
		},
		{
			Name:              "grant-support-root",
			Hidden:            true,
			Description:       "Grant or revoke exe.dev support root access to a box",
			Usage:             "grant-support-root <box-name> on|off",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
			Handler:           ss.handleGrantSupportRootCommand,
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
	if ss.server != nil {
		ct.DevMode = ss.server.env.ReplDev
	}
	return ct
}

func (ss *SSHServer) handleHelpCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.User != nil {
		ss.server.recordUserEventBestEffort(ctx, cc.User.ID, userEventHasRunHelp)
	}

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
	var boxes []exedb.Box
	err := ss.server.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		boxes, err = queries.BoxesForUser(ctx, cc.User.ID)
		return err
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		var boxList []map[string]any
		for _, b := range boxes {
			status := container.ContainerStatus(b.Status).String()
			box := map[string]any{
				"box_name": b.Name,
				"status":   status,
			}
			imageName := container.GetDisplayImageName(b.Image)
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

	if len(boxes) == 0 {
		cc.Write("No vms found. Create one with 'new'.\r\n")
		return nil
	}

	cc.Write("\033[1;36mYour vms:\033[0m\r\n")
	for _, b := range boxes {
		var statusColor string
		status := container.ContainerStatus(b.Status)
		switch status {
		case container.StatusRunning:
			statusColor = "\033[1;32m" // green
		case container.StatusStopped:
			statusColor = "\033[1;31m" // red
		case container.StatusPending:
			statusColor = "\033[1;33m" // yellow
		}
		cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", ss.server.env.BoxSub(b.Name), statusColor, status.String())
		imageName := container.GetDisplayImageName(b.Image)
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
	command := cc.FlagSet.Lookup("command").Value.String()
	prompt := cc.FlagSet.Lookup("prompt").Value.String()
	model := cc.FlagSet.Lookup("prompt-model").Value.String()
	noEmail := cc.FlagSet.Lookup("no-email").Value.String() == "true"
	noShard := cc.FlagSet.Lookup("no-shard").Value.String() == "true"

	// Parse environment variables
	var envVars []string
	if envFlag := cc.FlagSet.Lookup("env"); envFlag != nil {
		if repeatedEnv, ok := envFlag.Value.(*repeatedStringFlag); ok && repeatedEnv != nil {
			for _, env := range *repeatedEnv {
				// Validate format: must contain '=' and have non-empty key
				if !strings.Contains(env, "=") {
					return cc.Errorf("invalid environment variable format %q: must be KEY=VALUE", env)
				}
				parts := strings.SplitN(env, "=", 2)
				if parts[0] == "" {
					return cc.Errorf("invalid environment variable format %q: key cannot be empty", env)
				}
				envVars = append(envVars, env)
			}
		}
	}

	// Validate that --prompt is only used with exeuntu image
	if prompt != "" && image != "exeuntu" {
		return cc.Errorf("--prompt can only be used with the exeuntu image")
	}

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

	// If the box name matches the SSH username, reject it.
	// This avoids this common confusing scenario:
	//   $ ssh exe.dev  # implicitly: ssh mario@exe.dev
	//     > new mario
	//   $ ssh exe.dev  # goes to the new box instead of the repl
	if sess := cc.SSHSession; sess != nil && sess.User() == boxName {
		return cc.Errorf("New box name cannot match SSH username. To create a box named %v, ssh into this REPL with a different username and try again. If you do this, you will have to use a username other than %v to log in to the REPL going forward.", boxName, boxName)
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
	cc.Write("Creating \033[1m%s\033[0m using image \033[1m%s\033[0m...\r\n",
		boxName, displayImage)

	// Create channels for progress updates and completion
	type progressUpdate struct {
		info container.CreateProgressInfo
	}

	type instanceCompletion struct {
		container *container.Container
		err       error
	}
	progressChan := make(chan progressUpdate, 10)
	completionChan := make(chan instanceCompletion, 1)

	// Select exelet client
	exeletClient, exeletAddr, err := ss.server.selectExeletClient(cc.User.ID)
	if err != nil {
		return fmt.Errorf("failed to select exelet: %w", err)
	}

	// Pre-create box in database to get its ID
	imageToStore := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		imageToStore = "boldsoftware/exeuntu"
	}
	boxID, err := ss.server.preCreateBox(ctx, preCreateBoxOptions{
		userID:  user.ID,
		ctrhost: exeletAddr,
		name:    boxName,
		image:   imageToStore,
		noShard: noShard,
	})
	switch {
	case errors.Is(err, errNoIPShardsAvailable):
		// TODO: add CTA to upgrade plan
		// Since we don't have plans now...
		return cc.Errorf("You have reached the maximum number of boxes allowed on your plan.")
	case err != nil:
		return fmt.Errorf("failed to create box entry: %w", err)
	}

	// Start timing BEFORE creating instance
	startTime := time.Now()

	// Determine if we should show fancy output (spinners, colors, etc) BEFORE creating instance
	showSpinner := (ss.shouldShowSpinner(cc.SSHSession) || cc.ForceSpinner) && !cc.WantJSON()
	// Allow forced spinner (e.g., HTTP/SSE flows) via cc.ForceSpinner

	// Reserve space for spinner if we're showing it: print a blank line, then move cursor up.
	// This makes the readline prompt visible in the repl ui.
	if showSpinner {
		cc.Write("\r\n\033[1A")
	}

	exedevURL := ss.server.webBaseURLNoRequest()
	terminalURL := ss.server.xtermURL(boxName, ss.server.servingHTTPS())
	shelleyJSON := map[string]any{
		"terminal_url":  terminalURL,
		"default_model": shelleyDefaultModel,
	}
	// Use the metadata service for the gateway
	shelleyJSON["llm_gateway"] = "http://169.254.169.254/gateway/llm"
	// TODO: remove key_generator once exeuntu is rebuilt without it
	shelleyJSON["key_generator"] = "echo irrelevant"
	shelleyJSON["links"] = []map[string]string{
		{
			"title":    fmt.Sprintf("Back to %s", ss.server.env.WebHost),
			"icon_svg": "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6",
			"url":      exedevURL,
		},
	}
	shelleyConf, err := json.Marshal(shelleyJSON)
	if err != nil {
		return fmt.Errorf("error generating shelley config: %w", err)
	}

	// Generate SSH keys for the instance
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		return fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Extract host public key from the private key
	hostPrivKey, err := container.ParsePrivateKey(sshKeys.ServerIdentityKey)
	if err != nil {
		return fmt.Errorf("failed to parse host private key: %w", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create signer from host key: %w", err)
	}
	hostPublicKey := ssh.MarshalAuthorizedKey(hostSigner.PublicKey())

	// Run CreateInstance in background
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		// Expand image name to fully qualified reference (e.g., alpine -> docker.io/library/alpine:latest)
		fullImage := container.ExpandImageNameForContainerd(image)

		// Resolve tag to digest if tagResolver is available (for caching and consistency)
		imageRef := fullImage
		if ss.server.tagResolver != nil {
			platform := fmt.Sprintf("linux/%s", runtime.GOARCH)
			resolvedRef, err := ss.server.tagResolver.ResolveTag(ctx, fullImage, platform)
			if err != nil {
				slog.WarnContext(ctx, "Failed to resolve image tag, using tag directly", "image", fullImage, "error", err)
			} else {
				imageRef = resolvedRef
				slog.DebugContext(ctx, "Resolved image tag to digest", "tag", fullImage, "digest", imageRef)
			}
		}

		// Create instance request
		createReq := &api.CreateInstanceRequest{
			Name: boxName,
			// This ID leaks into exelet paths (e.g., the config paths).
			// We choose something that's unique (because boxID is a unique key
			// in the DB), but also legible to debugging, by including the boxName.
			// boxNames can be reused, so we can't just use that.
			ID: fmt.Sprintf("vm%06d-%s", boxID, boxName),

			Image:   imageRef,
			CPUs:    2,
			Memory:  ss.server.env.DefaultMemory,
			Disk:    ss.server.env.DefaultDisk,
			Env:     envVars,                // Environment variables
			SSHKeys: []string{cc.PublicKey}, // Pass user's SSH key
			Configs: []*api.Config{
				{
					Destination: "/exe.dev/shelley.json",
					Mode:        uint64(0o644),
					Source: &api.Config_File{
						File: &api.FileConfig{
							Data: shelleyConf,
						},
					},
				},
				{
					Destination: "/exe.dev/etc/ssh/ssh_host_ed25519_key",
					Mode:        uint64(0o600),
					Source: &api.Config_File{
						File: &api.FileConfig{
							Data: []byte(sshKeys.ServerIdentityKey),
						},
					},
				},
				{
					Destination: "/exe.dev/etc/ssh/ssh_host_ed25519_key.pub",
					Mode:        uint64(0o644),
					Source: &api.Config_File{
						File: &api.FileConfig{
							Data: hostPublicKey,
						},
					},
				},
				{
					Destination: "/exe.dev/etc/ssh/authorized_keys",
					Mode:        uint64(0o600),
					Source: &api.Config_File{
						File: &api.FileConfig{
							Data: []byte(sshKeys.AuthorizedKeys + cc.PublicKey),
						},
					},
				},
			},
		}

		// Call CreateInstance
		stream, err := exeletClient.client.CreateInstance(ctx, createReq)
		if err != nil {
			completionChan <- instanceCompletion{
				container: nil,
				err:       fmt.Errorf("failed to create instance: %w", err),
			}
			return
		}

		// Process stream responses
		var instance *api.Instance
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				completionChan <- instanceCompletion{
					container: nil,
					err:       fmt.Errorf("stream error: %w", err),
				}
				return
			}

			switch v := resp.Type.(type) {
			case *api.CreateInstanceResponse_Status:
				// Send progress update
				if showSpinner {
					info := mapExeletStatusToContainerProgress(v.Status)
					select {
					case progressChan <- progressUpdate{info}:
					default:
						// Channel full, skip this update
					}
				}
			case *api.CreateInstanceResponse_Instance:
				// Got the final instance
				instance = v.Instance
			}
		}

		if instance == nil {
			completionChan <- instanceCompletion{
				container: nil,
				err:       fmt.Errorf("no instance returned from the exelet"),
			}
			return
		}

		// Determine SSH user based on image
		// Check if this is an exeuntu image (handle various forms)
		sshUser := "root"
		if strings.Contains(image, "exeuntu") {
			sshUser = "exedev"
		}

		// Reconstruct ExposedPorts map from proto format
		exposedPorts := make(map[string]struct{})
		for _, ep := range instance.ExposedPorts {
			portSpec := fmt.Sprintf("%d/%s", ep.Port, ep.Protocol)
			exposedPorts[portSpec] = struct{}{}
		}

		// Map Instance to Container for compatibility
		createdContainer := &container.Container{
			ID:                   instance.ID,
			Name:                 instance.Name,
			SSHServerIdentityKey: sshKeys.ServerIdentityKey,
			SSHClientPrivateKey:  sshKeys.ClientPrivateKey,
			SSHPort:              int(instance.SSHPort),
			SSHUser:              sshUser,
			SSHAuthorizedKeys:    cc.PublicKey,
			ExposedPorts:         exposedPorts,
		}

		completionChan <- instanceCompletion{
			container: createdContainer,
			err:       nil,
		}
	}()

	// Track current progress state
	currentStatus := "Initializing"
	var imageSize int64
	var downloadedBytes int64
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIndex := 0

	// Main UI update loop
	// In CI, ticker only every 5s, because GitHub actions has a rough time
	// displaying a lot of text lines on error, and anyway, who wants to read all that?
	var ticker *time.Ticker
	if os.Getenv("CI") != "" {
		ticker = time.NewTicker(5 * time.Second)
	} else {
		ticker = time.NewTicker(100 * time.Millisecond)
	}
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
				currentStatus = "Starting box"
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
			slog.ErrorContext(ctx, "Failed to clean up box entry after container creation failure", "boxID", boxID, "error", err)
		}
		return createErr
	}

	// Update box with container info and SSH keys
	if createdContainer.SSHServerIdentityKey == "" {
		return fmt.Errorf("container created without SSH keys - this should not happen")
	}

	if err := ss.server.updateBoxWithContainer(ctx, boxID, createdContainer.ID, createdContainer.SSHUser, sshKeys, createdContainer.SSHPort); err != nil {
		return err
	}

	// Set up automatic routing based on exposed ports
	proxyPort := 80
	slog.DebugContext(ctx, "setting up automatic routing", "box", boxName, "exposed_ports", createdContainer.ExposedPorts)
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
				Name:            box.Name,
				CreatedByUserID: box.CreatedByUserID,
				Routes:          box.Routes,
			})
		}); err != nil {
			slog.WarnContext(ctx, "failed to save auto-routing setup", "box", boxName, "port", bestPort, "error", err)
		}
		proxyPort = bestPort
	}

	totalTime := time.Since(startTime)
	// Record user-perceived box creation time metric (observe only on success)
	if ss.server != nil && ss.server.sshMetrics != nil {
		ss.server.sshMetrics.boxCreationDur.Observe(totalTime.Seconds())
	}
	if showSpinner {
		// Clear the progress line and show formatted completion message
		cc.Write("\r\033[K")
	}
	details := newBoxDetails{
		BoxName:    boxName,
		SSHCommand: ss.server.boxSSHConnectionCommand(boxName),
		SSHServer:  ss.server.env.BoxHost,
		SSHPort:    ss.server.boxSSHPort(),
		SSHUser:    boxName,
		ProxyAddr:  ss.server.boxProxyAddress(boxName),
		ProxyPort:  proxyPort,
		VSCodeURL:  ss.server.vscodeURL(boxName),
		XTermURL:   ss.server.xtermURL(boxName, ss.server.servingHTTPS()),
	}
	// TODO(philip): We should allow Shelley to run on all images, but injecting it,
	// but, until that's done (https://github.com/boldsoftware/exe/issues/7), let's only
	// show the URL sometimes.
	// The strings.Contains check here is a miserable hack for e1e's TestNewWithPrompt. I am full of shame.
	if image == "exeuntu" && (command == "auto" || strings.Contains(command, "/usr/local/bin/shelley")) {
		details.ShelleyURL = ss.server.shelleyURL(boxName)
	}

	if !noEmail {
		go ss.server.sendBoxCreatedEmail(user.Email, details)
	}

	if cc.WantJSON() {
		cc.WriteJSON(details)
		return nil
	}
	var services [][3]string // [description, parenthetical, call to action]
	if details.ShelleyURL != "" {
		services = append(services,
			[3]string{"Coding agent", "", details.ShelleyURL},
		)
	}
	services = append(services,
		[3]string{"App", fmt.Sprintf("HTTPS proxy → :%d", details.ProxyPort), details.ProxyAddr},
		[3]string{"SSH", "", details.SSHCommand}, // show SSH last, to make it most prominent
	)
	if cc.IsInteractive() {
		cc.Write("Ready in %.1fs!\r\n\r\n", totalTime.Seconds())
		for _, svc := range services {
			parenthetical := ""
			if svc[1] != "" {
				parenthetical = " \033[2m(" + svc[1] + ")\033[0m"
			}
			cc.Write("\033[1m%s\033[0m%s\r\n%s\r\n\r\n", svc[0], parenthetical, svc[2])
		}
	} else {
		// Non-interactive session: output clean SSH command to stdout
		cc.Write("\r\n")
		for _, svc := range services {
			parenthetical := ""
			if svc[1] != "" {
				parenthetical = " (" + svc[1] + ")"
			}
			cc.Write("%s%s\r\n%s\r\n\r\n", svc[0], parenthetical, svc[2])
		}
	}

	// If prompt was provided, run it through Shelley
	if prompt != "" {
		cc.Write("\r\nSending prompt to Shelley...\r\n\r\n")

		// Get the box and SSH details for Shelley integration
		var box *exedb.Box
		if cc.PublicKey != "" {
			box, err = ss.server.getBoxForUser(ctx, cc.PublicKey, details.BoxName)
		} else {
			box, err = ss.server.getBoxForUserByUserID(ctx, cc.User.ID, details.BoxName)
		}
		if err != nil {
			return fmt.Errorf("failed to get box for Shelley: %w", err)
		}

		// Create SSH signer from the client private key
		sshSigner, err := container.CreateSSHSigner(sshKeys.ClientPrivateKey)
		if err != nil {
			return fmt.Errorf("failed to create SSH signer for Shelley: %w", err)
		}

		if model != "predictable" {
			prompt = shelleyPreamble + prompt
		}

		if err := ss.runShelleyPrompt(ctx, cc, box, sshSigner, prompt, details.ShelleyURL, model); err != nil {
			// We write out the error but don't fail.
			cc.WriteError("Error running Shelley prompt: %v", err)
			url := ss.server.shelleyURL(box.Name)
			cc.Write("Connect to Shelly at %s\r\n", url)
		}
		cc.Write("\r\n")
	}

	return nil
}

type newBoxDetails struct {
	BoxName    string `json:"box_name"`
	SSHCommand string `json:"ssh_command"`
	SSHServer  string `json:"ssh_server"`
	SSHPort    int    `json:"ssh_port"`
	SSHUser    string `json:"ssh_user"`
	ProxyAddr  string `json:"https_url"`
	ProxyPort  int    `json:"proxy_port"`
	ShelleyURL string `json:"shelley_url,omitempty"`
	VSCodeURL  string `json:"vscode_url,omitempty"`
	XTermURL   string `json:"xterm_url,omitempty"`
}

func (ss *SSHServer) handleDeleteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("please specify exactly one box name to delete, got %d", len(cc.Args))
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if err != nil {
		cc.WriteError("Box %q not found", boxName)
		return nil
	}

	cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)

	if err := ss.server.deleteBox(ctx, box); err != nil {
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
		if a.Current != b.Current {
			if a.Current {
				return -1
			}
			return 1
		}
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

func (ss *SSHServer) handleDeleteSSHKeyCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("please provide the SSH public key to delete")
	}
	publicKey := strings.Join(cc.Args, " ")
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" {
		return cc.Errorf("SSH public key cannot be empty")
	}

	// Canonicalize the public key.
	// This is dumb, but it means the key we attempt to delete here
	// matches the format stored in the database,
	// which is in the canonical form as presented
	// by ssh.MarshalAuthorizedKey during registration.
	parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return cc.Errorf("invalid SSH public key: %v", err)
	}
	canonicalKey := string(ssh.MarshalAuthorizedKey(parsedKey))

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_, err := queries.DeleteSSHKeyForUser(ctx, exedb.DeleteSSHKeyForUserParams{
			UserID:    cc.User.ID,
			PublicKey: canonicalKey,
		})
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("SSH key not found")
	}
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"public_key": strings.TrimSpace(canonicalKey),
			"status":     "deleted",
		})
		return nil
	}
	cc.Writeln("\033[1;32mDeleted SSH key.\033[0m")
	return nil
}

func (ss *SSHServer) handleProxyTokenCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("please specify exactly one box name")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found", boxName)
	}
	if err != nil {
		return err
	}

	token, err := ss.server.createProxyBearerToken(ctx, cc.User.ID, box.ID)
	if err != nil {
		return err
	}
	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"box_name": boxName,
			"token":    token,
		})
		return nil
	}

	cc.Writeln("This token may be used as a Bearer token or as a basic auth username to authenticate with the %s proxy for box \033[1m%s\033[0m.", ss.server.env.BoxHost, boxName)
	cc.Writeln("")
	cc.Writeln("%s", token)
	return nil
}

func (ss *SSHServer) handleBrowserCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	// Generate a verification token using the same system as email authentication.
	// The verification code for email is anti-phishing, but not needed here since the user directly acquires the link.
	token := generateRegistrationToken()

	// Store verification in database using the existing email verification table
	err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
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

	baseURL := ss.server.webBaseURLNoRequest()
	magicURL := fmt.Sprintf("%s/auth/verify?token=%s", baseURL, token)
	if cc.WantJSON() {
		magicLink := map[string]string{
			"magic_link": magicURL,
		}
		cc.WriteJSON(magicLink)
		return nil
	}
	cc.Writeln("This link will log you in to %s:", ss.server.env.WebHost)
	cc.Writeln("")
	cc.Writeln("\033[1;36m%s\033[0m", magicURL)
	cc.Writeln("")
	cc.Writeln("\033[2mExpires in 15 minutes.\033[0m")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleGrantSupportRootCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: grant-support-root <box-name> on|off")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
	onOff := strings.ToLower(cc.Args[1])

	var newValue int64
	switch onOff {
	case "on", "true", "1":
		newValue = 1
	case "off", "false", "0":
		newValue = 0
	default:
		return cc.Errorf("invalid value %q: use on or off", cc.Args[1])
	}

	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found", boxName)
	}
	if err != nil {
		return err
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.SetBoxSupportAccessAllowed(ctx, exedb.SetBoxSupportAccessAllowedParams{
			SupportAccessAllowed: newValue,
			ID:                   box.ID,
		})
	})
	if err != nil {
		return err
	}

	if newValue == 1 {
		cc.Writeln("exe.dev support now has root access to box %q.", boxName)
	} else {
		cc.Writeln("exe.dev support root access to box %q has been revoked.", boxName)
	}
	return nil
}

func (ss *SSHServer) completeBoxNames(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if ss == nil || ss.server == nil || len(ss.server.exeletClients) == 0 {
		return nil
	}
	if cc == nil || cc.User == nil {
		return nil
	}

	var boxes []exedb.Box
	err := ss.server.withRx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		boxes, err = queries.BoxesForUser(ctx, cc.User.ID)
		return err
	})
	if err != nil {
		return nil
	}

	var completions []string
	prefix := compCtx.CurrentWord
	for _, box := range boxes {
		if strings.HasPrefix(box.Name, prefix) {
			completions = append(completions, box.Name)
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

// mapExeletStatusToContainerProgress maps exelet CreateInstanceStatus to container progress
func mapExeletStatusToContainerProgress(status *api.CreateInstanceStatus) container.CreateProgressInfo {
	var phase container.CreateProgress
	switch status.State {
	case api.CreateInstanceStatus_INIT:
		phase = container.CreateInit
	case api.CreateInstanceStatus_NETWORK:
		phase = container.CreateInit
	case api.CreateInstanceStatus_PULLING:
		phase = container.CreatePull
	case api.CreateInstanceStatus_CONFIG:
		phase = container.CreateStart
	case api.CreateInstanceStatus_BOOT:
		phase = container.CreateStart
	case api.CreateInstanceStatus_COMPLETE:
		phase = container.CreateDone
	default:
		phase = container.CreateInit
	}

	return container.CreateProgressInfo{
		Phase: phase,
	}
}

// handleSSHCommand implements the ssh command - SSH into a box from the REPL
func (ss *SSHServer) handleSSHCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: ssh <box-name> [command...]")
	}

	name := cc.Args[0]
	cmdArgs := cc.Args[1:]

	// Trim the @host if present and validate it
	if _, found := strings.CutPrefix(name, "@"); found {
		// If they typed just @host with no boxname
		return cc.Errorf("usage: ssh <box-name> [command...]")
	} else if boxName, host, found := strings.Cut(name, "@"); found {
		// Format: boxname@host
		if host != ss.server.env.BoxHost {
			return cc.Errorf("unknown host %q; expected %s", host, ss.server.env.BoxHost)
		}
		name = boxName
	}

	// Also handle boxname.host format (e.g., "connx.exe.xyz")
	name = ss.normalizeBoxName(name)

	// Look up the box
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            name,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found", name)
	}
	if err != nil {
		return fmt.Errorf("failed to look up box: %w", err)
	}

	// Validate box has SSH credentials
	if box.SSHPort == nil || box.SSHUser == nil || len(box.SSHClientPrivateKey) == 0 {
		return cc.Errorf("box %q does not have SSH configured", name)
	}

	// Create SSH signer from the client private key
	sshSigner, err := ssh.ParsePrivateKey(box.SSHClientPrivateKey)
	if err != nil {
		return fmt.Errorf("failed to parse SSH key: %w", err)
	}

	sshHost := box.SSHHost()
	sshAddr := fmt.Sprintf("%s:%d", sshHost, *box.SSHPort)
	slog.InfoContext(ctx, "ssh command connecting to box", "addr", sshAddr, "user", *box.SSHUser, "ctrhost", box.Ctrhost)

	sshConfig := &ssh.ClientConfig{
		User: *box.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshSigner),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// Connect to the box with context support using net.Dialer
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", sshAddr)
	if err != nil {
		return cc.Errorf("failed to connect to box: %v", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, sshAddr, sshConfig)
	if err != nil {
		conn.Close()
		return cc.Errorf("SSH handshake failed: %v", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return cc.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	// Wire up stdout/stderr for all modes
	session.Stdout = cc.SSHSession
	session.Stderr = cc.SSHSession

	if len(cmdArgs) > 0 {
		// Exec mode - run command (no stdin needed)
		cmd := strings.Join(cmdArgs, " ")
		err := session.Run(cmd)
		cc.SSHSession.Write([]byte("\r")) // return cursor to column 0
		if err != nil {
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				// Return nil since we already wrote output; exit code is informational
				return nil
			}
			return cc.Errorf("command failed: %v", err)
		}
	} else {
		// Interactive mode - wire up stdin for the shell
		session.Stdin = cc.SSHSession

		// Get PTY info from the client session and set it up first
		// TODO(bmizerany): window change requests (glider mucks things up and makes ssh hard)
		pty, _, _ := cc.SSHSession.Pty()
		if err := session.RequestPty(
			// TODO(bmizerany): get actual terminal type from client (or env)? good enough for now
			"xterm-256color",

			pty.Window.Height,
			pty.Window.Width,
			nil,
		); err != nil {
			return cc.Errorf("failed to request PTY: %v", err)
		}

		// Interactive mode - start shell
		if err := session.Shell(); err != nil {
			return cc.Errorf("failed to start shell: %v", err)
		}
		session.Wait()
	}
	return nil
}
