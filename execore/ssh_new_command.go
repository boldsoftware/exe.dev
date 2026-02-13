package execore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/errorz"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/stage"

	"github.com/dustin/go-humanize"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (ss *SSHServer) handleNewCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	user := cc.User
	boxName := cc.FlagSet.Lookup("name").Value.String()
	image := cc.FlagSet.Lookup("image").Value.String()
	command := cc.FlagSet.Lookup("command").Value.String()
	prompt := cc.FlagSet.Lookup("prompt").Value.String()
	model := cc.FlagSet.Lookup("prompt-model").Value.String()
	noEmail := cc.IsSSHExec() || cc.FlagSet.Lookup("no-email").Value.String() == "true" || !ss.getUserDefaultNewVMEmail(ctx, cc.User.ID)
	noShard := cc.FlagSet.Lookup("no-shard").Value.String() == "true"
	exeletOverride := cc.FlagSet.Lookup("exelet").Value.String()

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

	image = strings.TrimSpace(image)
	if err := container.ValidateImageName(image); err != nil {
		return cc.Errorf("invalid image: %s", err)
	}

	// Validate that --prompt is only used with exeuntu image
	if prompt != "" && image != "exeuntu" {
		return cc.Errorf("--prompt can only be used with the exeuntu image")
	}

	// Handle --exelet override (support only)
	var exeletClient *exeletClient
	var exeletAddr string
	if exeletOverride != "" {
		if !ss.server.UserHasExeSudo(ctx, user.ID) {
			slog.WarnContext(ctx, "unauthorized exelet override attempt",
				"user_id", user.ID,
				"email", user.Email,
				"exelet", exeletOverride)
			return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", user.Email)
		}
		// Resolve short hostname to full address and get client
		exeletAddr, exeletClient = ss.server.resolveExelet(exeletOverride)
		if exeletClient == nil {
			return cc.Errorf("exelet %q not found. Available exelets: %v", exeletOverride, ss.server.exeletHostnames())
		}
	}

	// Parse and validate resource allocation flags
	memoryStr := cc.FlagSet.Lookup("memory").Value.String()
	diskStr := cc.FlagSet.Lookup("disk").Value.String()
	cpuVal, _ := strconv.ParseUint(cc.FlagSet.Lookup("cpu").Value.String(), 10, 64)

	// Set defaults from environment
	memory := ss.server.env.DefaultMemory
	disk := ss.server.env.DefaultDisk
	cpus := ss.server.env.DefaultCPUs

	// Get effective limits (team limits if in a team, otherwise user limits)
	effectiveLimits, _ := ss.server.GetEffectiveLimits(ctx, user.ID)

	// Determine max limits based on effective limits
	maxMemory := GetMaxMemory(ss.server.env, effectiveLimits)
	maxDisk := GetMaxDisk(ss.server.env, effectiveLimits)
	maxCPUs := GetMaxCPUs(ss.server.env, effectiveLimits)

	// Parse memory if provided
	if memoryStr != "" {
		parsedMemory, err := parseSize(memoryStr)
		if err != nil {
			return cc.Errorf("invalid --memory value: %s", err)
		}
		if parsedMemory < stage.MinMemory {
			return cc.Errorf("--memory must be at least %s", humanize.Bytes(stage.MinMemory))
		}
		if parsedMemory > maxMemory {
			return cc.Errorf("--memory cannot exceed %s", humanize.Bytes(maxMemory))
		}
		memory = parsedMemory
	}

	// Parse disk if provided
	if diskStr != "" {
		parsedDisk, err := parseSize(diskStr)
		if err != nil {
			return cc.Errorf("invalid --disk value: %s", err)
		}
		if parsedDisk < stage.MinDisk {
			return cc.Errorf("--disk must be at least %s", humanize.Bytes(stage.MinDisk))
		}
		if parsedDisk > maxDisk {
			return cc.Errorf("--disk cannot exceed %s", humanize.Bytes(maxDisk))
		}
		disk = parsedDisk
	}

	// Parse CPU if provided
	if cpuVal > 0 {
		if cpuVal < stage.MinCPUs {
			return cc.Errorf("--cpu must be at least %d", stage.MinCPUs)
		}
		if cpuVal > maxCPUs {
			return cc.Errorf("--cpu cannot exceed %d", maxCPUs)
		}
		cpus = cpuVal
	}

	// Check if user can create VMs (throttle, disabled, billing)
	if errMsg := ss.checkCanCreateVM(ctx, user, exeletOverride != ""); errMsg != "" {
		return cc.Errorf("%s", errMsg)
	}

	// Generate box name if not provided
	if boxName == "" {
		for range 10 {
			randBoxName := boxname.Random()
			if ss.server.isBoxNameAvailable(ctx, randBoxName) {
				boxName = randBoxName
				break
			}
		}
	}

	// Validate box name (both provided and generated)
	if err := boxname.Valid(boxName); err != nil {
		return cc.Errorf("%s", err)
	}

	if !ss.server.isBoxNameAvailable(ctx, boxName) {
		return cc.Errorf("VM name %q is not available", boxName)
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

	// Select exelet client (if not already set by --exelet override)
	if exeletClient == nil {
		var err error
		exeletClient, exeletAddr, err = ss.server.selectExeletClient(ctx, cc.User.ID)
		if err != nil {
			return fmt.Errorf("failed to select exelet: %w", err)
		}
	}

	// Pre-create box in database to get its ID
	imageToStore := container.GetDisplayImageName(image)
	if image == "exeuntu" {
		imageToStore = "boldsoftware/exeuntu"
	}
	boxID, err := ss.server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        user.ID,
		ctrhost:       exeletAddr,
		name:          boxName,
		image:         imageToStore,
		noShard:       noShard,
		region:        exeletClient.region.Code,
		allocatedCPUs: cpus,
	})
	switch {
	case errors.Is(err, errNoIPShardsAvailable):
		// TODO: add CTA to upgrade plan
		// Since we don't have plans now...
		return cc.Errorf("You have reached the maximum number of VMs allowed on your plan.")
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

	shelleyConf, err := ss.makeShelleyConfig(boxName)
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
		slog.InfoContext(ctx, "expanded image name", "input", image, "expanded", fullImage, "box", boxName)

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
		slog.InfoContext(ctx, "creating instance with image", "box", boxName, "imageRef", imageRef)

		// Create instance request
		createReq := &api.CreateInstanceRequest{
			Name: boxName,
			// This ID leaks into exelet paths (e.g., the config paths).
			// We choose something that's unique (because boxID is a unique key
			// in the DB), but also legible to debugging, by including the boxName.
			// boxNames can be reused, so we can't just use that.
			ID: fmt.Sprintf("vm%06d-%s", boxID, boxName),

			Image:   imageRef,
			CPUs:    cpus,
			Memory:  memory,
			Disk:    disk,
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
							Data: []byte("# This file is managed by exe.dev - do not modify\n" + sshKeys.AuthorizedKeys + cc.PublicKey),
						},
					},
				},
			},
			GroupID: cc.User.ID,
		}

		// Call CreateInstance
		exeletStart := time.Now()
		stream, err := exeletClient.client.CreateInstance(ctx, createReq)
		if err != nil {
			CommandLogAddDuration(ctx, "exelet_rpc", time.Since(exeletStart))
			// Check if this is a client error (user input issue)
			if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
				completionChan <- instanceCompletion{
					container: nil,
					err:       cc.Errorf("%s", s.Message()),
				}
				return
			}
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
				CommandLogAddDuration(ctx, "exelet_rpc", time.Since(exeletStart))
				// Check if this is a client error (user input issue)
				if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
					completionChan <- instanceCompletion{
						container: nil,
						err:       cc.Errorf("%s", s.Message()),
					}
					return
				}
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
		CommandLogAddDuration(ctx, "exelet_rpc", time.Since(exeletStart))

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
				currentStatus = "Starting VM"
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
		// Clean up the pre-created box entry and IP shard since container creation failed
		if err := ss.server.cleanupPreCreatedBox(ctx, boxID); err != nil {
			slog.ErrorContext(ctx, "Failed to clean up box entry after container creation failure", "boxID", boxID, "error", err)
		}
		// Check if this is a gRPC user error (e.g., invalid image name)
		// and convert it to a CommandClientError for proper display.
		if gs, ok := errorz.AsType[grpcStatuser](createErr); ok {
			switch gs.GRPCStatus().Code() {
			case codes.InvalidArgument, codes.FailedPrecondition:
				return cc.Errorf("%s", gs.GRPCStatus().Message())
			}
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
			// Fallback for non-SSH contexts (e.g., web flow) where PublicKey is empty
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
		if err := ss.updateBoxRouteInDB(ctx, box.Name, box.CreatedByUserID, box.Routes, route.Port, route.Share); err != nil {
			slog.WarnContext(ctx, "failed to save auto-routing setup", "box", boxName, "port", bestPort, "error", err)
		}
		proxyPort = bestPort
	}

	totalTime := time.Since(startTime)
	// Record user-perceived box creation time metric (observe only on success)
	if ss.server != nil && ss.server.sshMetrics != nil {
		ss.server.sshMetrics.boxCreationDur.Observe(totalTime.Seconds())
	}
	ss.server.slackFeed.CreatedVM(ctx, user.ID)

	if showSpinner {
		// Clear the progress line and show formatted completion message
		cc.Write("\r\033[K")
	}
	details := newBoxDetails{
		VMName:     boxName,
		SSHDest:    ss.server.env.BoxDest(boxName),
		SSHCommand: ss.server.boxSSHConnectionCommand(boxName),
		SSHPort:    ss.server.boxSSHPort(),
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
		go ss.server.sendBoxCreatedEmail(context.Background(), user.Email, details)
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
			box, err = ss.server.getBoxForUser(ctx, cc.PublicKey, details.VMName)
		} else {
			box, err = ss.server.getBoxForUserByUserID(ctx, cc.User.ID, details.VMName)
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
