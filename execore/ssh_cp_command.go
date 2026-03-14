package execore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/stage"

	"github.com/dustin/go-humanize"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// handleCpCommand implements the cp command - copy an existing VM using ZFS snapshots
func (ss *SSHServer) handleCpCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	user := cc.User

	// Validate arguments - need 1 or 2 positional args (source VM, optional dest name)
	if len(cc.Args) < 1 || len(cc.Args) > 2 {
		return cc.Errorf("usage: cp <source-vm> [new-name]")
	}

	sourceVMName := ss.normalizeBoxName(cc.Args[0])
	var newName string
	if len(cc.Args) == 2 {
		newName = cc.Args[1]
	}

	// Check if user can create VMs (throttle, disabled, billing)
	if errMsg := ss.checkCanCreateVM(ctx, user, false); errMsg != "" {
		return cc.Errorf("%s", errMsg)
	}

	// Check if the source box exists and user has access (owner or team owner)
	sourceBox, _, err := ss.server.FindAccessibleBox(ctx, user.ID, sourceVMName)
	if err != nil {
		return cc.Errorf("vm %q not found", sourceVMName)
	}

	// Ensure source VM has a container
	if sourceBox.ContainerID == nil {
		return cc.Errorf("vm %q has no container to copy", sourceVMName)
	}

	// Parse and validate resource allocation flags
	memoryStr := cc.FlagSet.Lookup("memory").Value.String()
	diskStr := cc.FlagSet.Lookup("disk").Value.String()
	cpuVal, _ := strconv.ParseUint(cc.FlagSet.Lookup("cpu").Value.String(), 10, 64)

	// Get effective limits (team limits if in a team, otherwise user limits)
	effectiveLimits, _ := ss.server.GetEffectiveLimits(ctx, user.ID)

	// Determine max limits based on effective limits
	maxMemory := GetMaxMemory(ss.server.env, effectiveLimits)
	maxDisk := GetMaxDisk(ss.server.env, effectiveLimits)
	maxCPUs := GetMaxCPUs(ss.server.env, effectiveLimits)

	// Resource overrides for clone (0 = use source)
	var memoryOverride, diskOverride, cpuOverride uint64

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
		memoryOverride = parsedMemory
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
		diskOverride = parsedDisk
	}

	// Parse CPU if provided
	if cpuVal > 0 {
		if cpuVal < stage.MinCPUs {
			return cc.Errorf("--cpu must be at least %d", stage.MinCPUs)
		}
		if cpuVal > maxCPUs {
			return cc.Errorf("--cpu cannot exceed %d", maxCPUs)
		}
		cpuOverride = cpuVal
	}

	// Generate clone name if not provided
	if newName == "" {
		for range 10 {
			randBoxName := boxname.Random()
			if ss.server.isBoxNameAvailableForUser(ctx, randBoxName, user.ID) {
				newName = randBoxName
				break
			}
		}
		if newName == "" {
			return cc.Errorf("failed to generate a unique name for the copy")
		}
	}

	// Validate clone name
	if err := boxname.Valid(newName); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}

	// Check if clone name is available for this user
	if !ss.server.isBoxNameAvailableForUser(ctx, newName, user.ID) {
		return cc.Errorf("name %q is not available", newName)
	}

	// Get exelet client for the source box (clone must happen on same exelet)
	exeletClient := ss.server.getExeletClient(sourceBox.Ctrhost)
	if exeletClient == nil {
		return cc.Errorf("exelet host not available for source VM")
	}
	exeletAddr := sourceBox.Ctrhost

	// Pre-create box in database
	// Determine allocated CPUs for the clone
	cloneCPUs := ss.server.env.DefaultCPUs
	if sourceBox.AllocatedCpus != nil {
		cloneCPUs = uint64(*sourceBox.AllocatedCpus)
	}
	if cpuOverride > 0 {
		cloneCPUs = cpuOverride
	}
	boxID, err := ss.server.preCreateBox(ctx, preCreateBoxOptions{
		userID:        user.ID,
		ctrhost:       exeletAddr,
		name:          newName,
		image:         sourceBox.Image,
		noShard:       false,
		region:        sourceBox.Region,
		allocatedCPUs: cloneCPUs,
	})
	switch {
	case errors.Is(err, errNoIPShardsAvailable):
		return cc.Errorf("You have reached the maximum number of VMs allowed on your plan.")
	case err != nil:
		return fmt.Errorf("failed to create box entry: %w", err)
	}

	// Show copying message
	if !cc.WantJSON() {
		cc.Write("Copying \033[1m%s\033[0m to \033[1m%s\033[0m...\r\n", sourceVMName, newName)
	}

	// Start timing
	startTime := time.Now()

	// Determine if we should show spinner
	showSpinner := (ss.shouldShowSpinner(cc.SSHSession) || cc.ForceSpinner) && !cc.WantJSON()

	// Reserve space for spinner
	if showSpinner {
		cc.Write("\r\n\033[1A")
	}

	// Generate SSH keys for the new instance
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		// Clean up pre-created box
		_ = ss.server.cleanupPreCreatedBox(ctx, boxID)
		return fmt.Errorf("failed to generate SSH keys: %w", err)
	}

	// Extract host public key
	hostPrivKey, err := container.ParsePrivateKey(sshKeys.ServerIdentityKey)
	if err != nil {
		_ = ss.server.cleanupPreCreatedBox(ctx, boxID)
		return fmt.Errorf("failed to parse host private key: %w", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivKey)
	if err != nil {
		_ = ss.server.cleanupPreCreatedBox(ctx, boxID)
		return fmt.Errorf("failed to create signer from host key: %w", err)
	}
	hostPublicKey := ssh.MarshalAuthorizedKey(hostSigner.PublicKey())

	shelleyConf, err := ss.makeShelleyConfig(newName)
	if err != nil {
		_ = ss.server.cleanupPreCreatedBox(ctx, boxID)
		return fmt.Errorf("error generating shelley config: %w", err)
	}

	// Create clone request
	cloneReq := &api.CloneInstanceRequest{
		SourceInstanceID: *sourceBox.ContainerID,
		NewInstanceID:    fmt.Sprintf("vm%06d-%s", boxID, newName),
		NewName:          newName,
		GroupID:          user.ID,
		SSHKeys:          []string{cc.PublicKey},
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
				Mode:        uint64(0o644),
				Source: &api.Config_File{
					File: &api.FileConfig{
						Data: []byte("# This file is managed by exe.dev - do not modify\n" + sshKeys.AuthorizedKeys + cc.PublicKey),
					},
				},
			},
		},
	}

	// Add resource overrides if specified
	if memoryOverride > 0 {
		cloneReq.Memory = &memoryOverride
	}
	if diskOverride > 0 {
		cloneReq.Disk = &diskOverride
	}
	if cpuOverride > 0 {
		cloneReq.CPUs = &cpuOverride
	}

	// Create channels for progress and completion
	type cloneCompletion struct {
		instance *api.Instance
		err      error
	}
	completionChan := make(chan cloneCompletion, 1)

	// Run CloneInstance in background
	go func() {
		cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		stream, err := exeletClient.client.CloneInstance(cloneCtx, cloneReq)
		if err != nil {
			completionChan <- cloneCompletion{nil, fmt.Errorf("failed to start clone: %w", err)}
			return
		}

		var instance *api.Instance
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
					completionChan <- cloneCompletion{nil, cc.Errorf("%s", s.Message())}
					return
				}
				completionChan <- cloneCompletion{nil, fmt.Errorf("clone stream error: %w", err)}
				return
			}

			switch v := resp.Type.(type) {
			case *api.CloneInstanceResponse_Status:
				// Could update progress here if needed
				_ = v.Status
			case *api.CloneInstanceResponse_Instance:
				instance = v.Instance
			}
		}

		if instance == nil {
			completionChan <- cloneCompletion{nil, fmt.Errorf("no instance returned from clone")}
			return
		}

		completionChan <- cloneCompletion{instance, nil}
	}()

	// Progress display loop
	currentStatus := "Copying"
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIndex := 0

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var clonedInstance *api.Instance
	var cloneErr error

	for {
		select {
		case result := <-completionChan:
			clonedInstance = result.instance
			cloneErr = result.err
			goto done

		case <-ticker.C:
			if showSpinner {
				elapsed := time.Since(startTime)
				spinner := spinners[spinnerIndex%len(spinners)]
				spinnerIndex++
				cc.Write("\r\033[K%s %.1fs %s...", spinner, elapsed.Seconds(), currentStatus)
			}
		}
	}

done:
	// Clear progress line
	if showSpinner {
		cc.Write("\r\033[K")
	}

	if cloneErr != nil {
		// Clean up pre-created box and IP shard
		if err := ss.server.cleanupPreCreatedBox(ctx, boxID); err != nil {
			slog.ErrorContext(ctx, "Failed to clean up box entry after clone failure", "boxID", boxID, "error", err)
		}
		var gs grpcStatuser
		if errors.As(cloneErr, &gs) {
			switch gs.GRPCStatus().Code() {
			case codes.InvalidArgument, codes.FailedPrecondition:
				return cc.Errorf("%s", gs.GRPCStatus().Message())
			}
		}
		return cloneErr
	}

	// Determine SSH user based on source image
	sshUser := "root"
	if strings.Contains(sourceBox.Image, "exeuntu") {
		sshUser = "exedev"
	}

	// Update box with container info
	if err := ss.server.updateBoxWithContainer(ctx, boxID, clonedInstance.ID, sshUser, sshKeys, int(clonedInstance.SSHPort)); err != nil {
		return err
	}

	// Copy routing from source box if available
	if sourceBox.Routes != nil {
		route := sourceBox.GetRoute()
		if err := ss.updateBoxRouteInDB(ctx, newName, user.ID, sourceBox.Routes, route.Port, route.Share); err != nil {
			slog.WarnContext(ctx, "failed to copy routing from source box", "source", sourceVMName, "clone", newName, "error", err)
		}
	}

	totalTime := time.Since(startTime)
	ss.server.slackFeed.CreatedVM(ctx, user.ID)
	ss.server.autoThrottleVMCreation(ctx)

	// Return result
	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"name":    newName,
			"state":   "starting",
			"source":  sourceVMName,
			"ssh":     ss.server.env.BoxDest(newName),
			"elapsed": totalTime.Seconds(),
		})
		return nil
	}

	// Show completion message
	cc.Write("Created \033[1m%s\033[0m from \033[1m%s\033[0m in %.1fs\r\n", newName, sourceVMName, totalTime.Seconds())
	cc.Write("\r\n")
	cc.Write("\033[1m%s\033[0m\r\n\r\n", ss.server.boxProxyAddress(newName))
	cc.Write("ssh \033[1m%s\033[0m\r\n\r\n", ss.server.env.BoxDest(newName))

	return nil
}
