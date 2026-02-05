package execore

import (
	"cmp"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/errorz"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/region"
	"exe.dev/sshpool2"
	"exe.dev/stage"
)

// grpcStatuser is an interface for errors that wrap a gRPC status.
// Use with errors.As to extract the underlying status from wrapped errors.
type grpcStatuser interface {
	error
	GRPCStatus() *status.Status
}

// TODO(philip): Probably can be done in Shelley itself as part of the system prompt.
const shelleyPreamble = `
The user has just created this VM, and wants to do the following with it.
`

const shelleyDefaultModel = "claude-opus-4.5"

// makeShelleyConfig generates the shelley.json config for a box.
func (ss *SSHServer) makeShelleyConfig(boxName string) ([]byte, error) {
	exedevURL := ss.server.webBaseURLNoRequest()
	terminalURL := ss.server.xtermURL(boxName, ss.server.servingHTTPS())
	shelleyJSON := map[string]any{
		"terminal_url":  terminalURL + "?d=WORKING_DIR",
		"default_model": shelleyDefaultModel,
		"llm_gateway":   "http://169.254.169.254/gateway/llm",
		"key_generator": "echo irrelevant", // TODO: remove once exeuntu is rebuilt without it
		"links": []map[string]string{
			{
				"title":    fmt.Sprintf("Back to %s", ss.server.env.WebHost),
				"icon_svg": "M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6",
				"url":      exedevURL,
			},
			{
				"title":    ss.server.env.BoxSub(boxName),
				"icon_svg": "M13.19 8.688a4.5 4.5 0 011.242 7.244l-4.5 4.5a4.5 4.5 0 01-6.364-6.364l1.757-1.757m13.35-.622l1.757-1.757a4.5 4.5 0 00-6.364-6.364l-4.5 4.5a4.5 4.5 0 001.242 7.244",
				"url":      ss.server.boxProxyAddress(boxName),
			},
		},
	}
	return json.Marshal(shelleyJSON)
}

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

// statusColor returns an ANSI color code for a container status.
func statusColor(s container.ContainerStatus) string {
	switch s {
	case container.StatusRunning:
		return "\033[1;32m" // green
	case container.StatusStopped:
		return "\033[1;31m" // red
	case container.StatusPending:
		return "\033[1;33m" // yellow
	default:
		return ""
	}
}

// jsonOnlyFlags returns a FlagSet creation function for a FlagSet named name with only the --json flag.
func jsonOnlyFlags(name string) func() *flag.FlagSet {
	return func() *flag.FlagSet {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.Bool("json", false, "output in JSON format")
		return fs
	}
}

// addBoolFlag wraps a FlagSet creation function to add a boolean flag.
func addBoolFlag(name, usage string) func(func() *flag.FlagSet) func() *flag.FlagSet {
	return func(f func() *flag.FlagSet) func() *flag.FlagSet {
		return func() *flag.FlagSet {
			fs := f()
			fs.Bool(name, false, usage)
			return fs
		}
	}
}

// addStringFlag wraps a FlagSet creation function to add a string flag.
func addStringFlag(name, defaultVal, usage string) func(func() *flag.FlagSet) func() *flag.FlagSet {
	return func(f func() *flag.FlagSet) func() *flag.FlagSet {
		return func() *flag.FlagSet {
			fs := f()
			fs.String(name, defaultVal, usage)
			return fs
		}
	}
}

var (
	addQRFlag           = addBoolFlag("qr", "show QR code for the URL")
	addLongFlag         = addBoolFlag("l", "show detailed information")
	addShareMessageFlag = addStringFlag("message", "", "message to include in share invitation")
)

// parseSize parses a human-readable size string into bytes using humanize.ParseBytes.
// Numbers without a suffix are treated as GB (e.g., "4" means 4GB).
func parseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Try parsing as plain number first (treat as GB)
	if bytes, err := strconv.ParseUint(s, 10, 64); err == nil {
		return bytes * 1024 * 1024 * 1024, nil // Convert GB to bytes
	}

	// Try parsing with unit suffix using humanize
	return humanize.ParseBytes(s)
}

// newCommandFlags creates a FlagSet for the new command
func newCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.String("name", "", "VM name (auto-generated if not specified)")
	fs.String("image", "exeuntu", "container image")
	fs.String("command", "auto", "container command: auto, none, or a custom command")
	fs.String("prompt", "", "initial prompt to send to Shelley after VM creation (requires exeuntu image)")
	fs.Bool("json", false, "output in JSON format")
	fs.Bool("no-email", false, "do not send email notification")
	fs.String("prompt-model", shelleyDefaultModel, "[hidden] override the prompt model") // for testing
	fs.Bool("no-shard", false, "[hidden] skip shard allocation")
	fs.String("exelet", "", "[hidden] create VM on specified exelet (support only)")
	// Resource allocation flags (defaults: 8GB memory, 20GB disk, 2 CPUs)
	fs.String("memory", "", "[hidden] memory allocation (e.g., 4, 4GB, 8G)")
	fs.String("disk", "", "[hidden] disk size (e.g., 20, 20GB, 50G)")
	fs.Uint("cpu", 0, "[hidden] number of CPUs (default 2)")
	// Environment variables (can be specified multiple times)
	var envVars repeatedStringFlag
	fs.Var(&envVars, "env", "environment variable in KEY=VALUE format (can be specified multiple times)")
	return fs
}

func resizeCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("resize", flag.ContinueOnError)
	fs.String("memory", "", "memory allocation (e.g., 4, 4GB, 8G)")
	fs.Uint("cpu", 0, "number of CPUs")
	fs.String("disk", "", "new total disk size (e.g., 25, 25GB) - must be larger than current size")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

//go:generate go run ../cmd/gencmddocs

// NewCommandTree creates a new command tree with all exe.dev commands
func NewCommandTree(ss *SSHServer) *exemenu.CommandTree {
	commands := []*exemenu.Command{
		{
			Name:              "help",
			Description:       "Show help information",
			Handler:           ss.handleHelpCommand,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeCommandNames,
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
			Name:              "ls",
			Description:       "List your VMs",
			Handler:           ss.handleListCommand,
			FlagSetFunc:       addLongFlag(jsonOnlyFlags("ls")),
			Usage:             "ls [-l] [name|pattern]",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		{
			Name:        "new",
			Description: "Create a new VM",
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
			Description:       "Delete a VM",
			Handler:           ss.handleDeleteCommand,
			FlagSetFunc:       jsonOnlyFlags("rm"),
			Usage:             "rm <vmname>...",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		{
			Name:              "restart",
			Description:       "Restart a VM",
			Handler:           ss.handleRestartCommand,
			FlagSetFunc:       jsonOnlyFlags("restart"),
			Usage:             "restart <vmname>",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
		},
		// 'rename' is separated from the 'mv' command as it is different enough both
		// semantically and in implementation - they are different enough in this
		// "VM move/rename" context that having a different command for the two operations
		// fits the *nix "do one thing and do it well" mantra better than if they were the same
		{
			Name:              "rename",
			Hidden:            false,
			Description:       "rename a vm",
			Usage:             "rename <oldname> <newname>",
			FlagSetFunc:       jsonOnlyFlags("rename"),
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeRenameArgs,
			Handler:           ss.handleRenameCommand,
		},
		{
			Name:              "cp",
			Description:       "Copy an existing VM",
			Usage:             "cp <source-vm> [new-name]",
			FlagSetFunc:       cpCommandFlags,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
			Handler:           ss.handleCpCommand,
			Examples: []string{
				"cp my-vm              # copy with auto-generated name",
				"cp my-vm my-vm-copy   # copy with specific name",
			},
		},
		{
			Name:        "hireme",
			Aliases:     boxname.JobsRelated,
			Hidden:      true,
			Description: "Apply for a job at exe.dev",
			Handler:     ss.handleJobCommand,
		},
		{
			Name:        "sudo",
			Hidden:      true,
			Description: "Execute a command as another user",
			Handler:     ss.handleSudoCommand,
			RawArgs:     true,
		},
		ss.shareCommand(),
		{
			Name:        "whoami",
			Description: "Show your user information including email and all SSH keys.",
			Usage:       "whoami",
			Handler:     ss.handleWhoamiCommand,
			FlagSetFunc: jsonOnlyFlags("whoami"),
		},
		ss.sshKeyCommand(),
		{
			Name:              "delete-ssh-key",
			Hidden:            true,
			Description:       "Delete an SSH key (deprecated: use 'ssh-key remove' instead)",
			Usage:             "delete-ssh-key <public-key>",
			Handler:           ss.handleSSHKeyRemoveCmd,
			FlagSetFunc:       jsonOnlyFlags("delete-ssh-key"),
			HasPositionalArgs: true,
		},
		ss.defaultsCommand(),
		ss.shelleyCommand(),
		{
			Name:        "browser",
			Description: "Generate a magic link to log in to the website",
			Usage:       "browser",
			Handler:     ss.handleBrowserCommand,
			FlagSetFunc: addQRFlag(jsonOnlyFlags("browser")),
		},
		{
			Name:              "ssh",
			Description:       "SSH into a VM",
			Usage:             "ssh <vmname> [command...]",
			Handler:           ss.handleSSHCommand,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
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
			Name:        "true",
			Description: "Do nothing, successfully",
			Hidden:      true,
			Handler:     func(ctx context.Context, cc *exemenu.CommandContext) error { return nil },
		},
		{
			Name:              "grant-support-root",
			Hidden:            true,
			Description:       "Grant or revoke exe.dev support root access to a VM",
			Usage:             "grant-support-root <vmname> on|off",
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
			Handler:           ss.handleGrantSupportRootCommand,
		},
		{
			Name:        "exelets",
			Hidden:      true,
			Description: "List all exelets (support only)",
			FlagSetFunc: jsonOnlyFlags("exelets"),
			Handler:     ss.handleExeletsCommand,
		},
		{
			Name:              "resize",
			Hidden:            true,
			Description:       "Resize a VM's memory, CPU, or disk (support only)",
			Usage:             "resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>]",
			HasPositionalArgs: true,
			FlagSetFunc:       resizeCommandFlags,
			Handler:           ss.handleResizeCommand,
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
	wantLong := cc.FlagSet.Lookup("l").Value.String() == "true"

	// Disallow --json and -l together
	if cc.WantJSON() && wantLong {
		return cc.Errorf("cannot use --json and -l together")
	}

	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxesForUser, cc.User.ID)
	if err != nil {
		return err
	}

	// Filter by name or glob pattern if specified
	if len(cc.Args) > 0 {
		var filtered []exedb.Box
		for _, arg := range cc.Args {
			if pattern.HasMeta(arg, 0) {
				// Shell glob pattern
				reStr, err := pattern.Regexp(arg, pattern.EntireString)
				if err != nil {
					return cc.Errorf("invalid pattern %q: %v", arg, err)
				}
				re, err := regexp.Compile(reStr)
				if err != nil {
					return cc.Errorf("invalid pattern %q: %v", arg, err)
				}
				for _, b := range boxes {
					if re.MatchString(b.Name) {
						filtered = append(filtered, b)
					}
				}
			} else {
				// Literal name
				name := ss.normalizeBoxName(arg)
				if idx := slices.IndexFunc(boxes, func(b exedb.Box) bool { return b.Name == name }); idx >= 0 {
					filtered = append(filtered, boxes[idx])
				}
			}
		}
		slices.SortFunc(filtered, func(a, b exedb.Box) int { return cmp.Compare(a.Name, b.Name) })
		boxes = slices.CompactFunc(filtered, func(a, b exedb.Box) bool { return a.Name == b.Name })
	}

	if cc.WantJSON() {
		var vmList []map[string]any
		for _, vm := range boxes {
			status := container.ContainerStatus(vm.Status).String()
			box := map[string]any{
				"vm_name":  vm.Name,
				"ssh_dest": ss.server.env.BoxDest(vm.Name),
				"status":   status,
				"region":   vm.Region,
			}
			if r, err := region.ByCode(vm.Region); err == nil {
				box["region_display"] = r.Display
			}
			imageName := container.GetDisplayImageName(vm.Image)
			switch imageName {
			case "exeuntu", "":
			default:
				box["image"] = imageName
			}
			vmList = append(vmList, box)
		}
		cc.WriteJSON(map[string]any{
			"vms": vmList,
		})
		return nil
	}

	if len(boxes) == 0 {
		cc.Write("No VMs found. Create one with 'new'.\r\n")
		return nil
	}

	if wantLong {
		// Long format: one line per VM with all details, tabwriter-aligned
		tw := tabwriter.NewWriter(cc.Output, 0, 0, 2, ' ', 0)
		for _, b := range boxes {
			status := container.ContainerStatus(b.Status)
			shelleyURL := "-"
			if strings.Contains(b.Image, "exeuntu") {
				shelleyURL = ss.server.shelleyURL(b.Name)
			}
			fmt.Fprintf(tw, "\033[1m%s\033[0m\t%s%s\033[0m\t%s\t%s\t%s\r\n",
				ss.server.env.BoxSub(b.Name),
				statusColor(status), status,
				b.Region,
				shelleyURL,
				ss.server.boxProxyAddress(b.Name),
			)
		}
		tw.Flush()
		return nil
	}

	cc.Write("\033[1;36mYour VMs:\033[0m\r\n")
	for _, b := range boxes {
		status := container.ContainerStatus(b.Status)
		cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", ss.server.env.BoxSub(b.Name), statusColor(status), status)
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
	if exeletOverride != "" {
		if !ss.server.UserHasExeSudo(ctx, user.ID) {
			slog.WarnContext(ctx, "unauthorized exelet override attempt",
				"user_id", user.ID,
				"email", user.Email,
				"exelet", exeletOverride)
			return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", user.Email)
		}
		// Validate that the exelet is in the available list
		if ss.server.getExeletClient(exeletOverride) == nil {
			return cc.Errorf("exelet %q not found. Available exelets: %v", exeletOverride, ss.server.exeletAddrs())
		}
	}

	// Parse and validate resource allocation flags
	memoryStr := cc.FlagSet.Lookup("memory").Value.String()
	diskStr := cc.FlagSet.Lookup("disk").Value.String()
	cpuVal, _ := strconv.ParseUint(cc.FlagSet.Lookup("cpu").Value.String(), 10, 64)

	// Determine if user has support privileges for higher limits
	isSupport := ss.server.UserHasExeSudo(ctx, user.ID)

	// Set defaults from environment
	memory := ss.server.env.DefaultMemory
	disk := ss.server.env.DefaultDisk
	cpus := ss.server.env.DefaultCPUs

	// Determine max limits based on user type
	// For normal users, max is the environment default (but at least the minimum)
	// For support users, max is the higher support limit
	maxMemory := max(ss.server.env.DefaultMemory, stage.MinMemory)
	maxDisk := max(ss.server.env.DefaultDisk, stage.MinDisk)
	maxCPUs := max(ss.server.env.DefaultCPUs, uint64(stage.MinCPUs))
	if isSupport {
		maxMemory = stage.SupportMaxMemory
		maxDisk = stage.SupportMaxDisk
		maxCPUs = stage.SupportMaxCPUs
	}

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

	// Select exelet client
	var exeletClient *exeletClient
	var exeletAddr string
	if exeletOverride != "" {
		exeletAddr = exeletOverride
		exeletClient = ss.server.exeletClients[exeletOverride]
	} else {
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
		userID:  user.ID,
		ctrhost: exeletAddr,
		name:    boxName,
		image:   imageToStore,
		noShard: noShard,
		region:  exeletClient.region.Code,
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
		// Clean up the pre-created box entry since container creation failed
		if err := withTx1(ss.server, context.WithoutCancel(ctx), (*exedb.Queries).DeleteBox, boxID); err != nil {
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
		if err := withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxRoutes, exedb.UpdateBoxRoutesParams{
			Name:            box.Name,
			CreatedByUserID: box.CreatedByUserID,
			Routes:          box.Routes,
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

type newBoxDetails struct {
	VMName     string `json:"vm_name"`
	SSHCommand string `json:"ssh_command"`
	SSHDest    string `json:"ssh_dest"`
	SSHPort    int    `json:"ssh_port"`
	ProxyAddr  string `json:"https_url"`
	ProxyPort  int    `json:"proxy_port"`
	ShelleyURL string `json:"shelley_url,omitempty"`
	VSCodeURL  string `json:"vscode_url,omitempty"`
	XTermURL   string `json:"xterm_url,omitempty"`
}

func (ss *SSHServer) handleRestartCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("please specify exactly one VM name to restart, got %d", len(cc.Args))
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Verify the box exists and belongs to this user
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if err != nil {
		return cc.Errorf("VM %q not found", boxName)
	}

	if box.ContainerID == nil {
		return cc.Errorf("VM %q has no container", boxName)
	}

	exeletClient := ss.server.getExeletClient(box.Ctrhost)
	if exeletClient == nil {
		return cc.Errorf("exelet host not available for VM")
	}

	cc.Writeln("Restarting \033[1m%s\033[0m...", boxName)

	// Use WithoutCancel so the restart completes even if client disconnects
	restartCtx := context.WithoutCancel(ctx)
	containerID := *box.ContainerID

	// Get the current instance state to decide whether to stop first
	const maxAttempts = 3
	instanceResp, err := exeletClient.client.GetInstance(restartCtx, &api.GetInstanceRequest{
		ID: containerID,
	})
	if err != nil {
		return fmt.Errorf("failed to get instance status: %w", err)
	}

	// Only stop if the instance is currently running
	state := instanceResp.Instance.State
	switch state {
	case api.VMState_RUNNING, api.VMState_STARTING, api.VMState_PAUSED:
		// Instance is up, need to stop it first
		var stopErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			_, stopErr = exeletClient.client.StopInstance(restartCtx, &api.StopInstanceRequest{
				ID: containerID,
			})
			if stopErr == nil {
				break
			}
			// State changed since we checked - that's OK
			if s, ok := status.FromError(stopErr); ok && s.Code() == codes.FailedPrecondition {
				stopErr = nil
				break
			}
			if attempt < maxAttempts {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if stopErr != nil {
			return fmt.Errorf("failed to stop instance: %w", stopErr)
		}
		// Drop any pooled SSH connections after stopping so proxy requests fail fast
		// and retry with fresh connections after restart.
		if box.SSHPort != nil {
			ss.server.sshPool.DropConnectionsTo(box.SSHHost(), int(*box.SSHPort))
		}
	case api.VMState_STOPPED, api.VMState_ERROR, api.VMState_CREATED:
		// Instance is already stopped or in a restartable state, skip stop.
		// But still drop any pooled SSH connections - they may be stale from
		// before the VM was stopped (e.g., poweroff from inside the VM).
		if box.SSHPort != nil {
			ss.server.sshPool.DropConnectionsTo(box.SSHHost(), int(*box.SSHPort))
		}
	default:
		// Unknown or transient state (STOPPING, CREATING, UPDATING, DELETED)
		return cc.Errorf("VM is in state %q and cannot be restarted", state.String())
	}

	// Start the instance with retries
	var startErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, startErr = exeletClient.client.StartInstance(restartCtx, &api.StartInstanceRequest{
			ID: containerID,
		})
		if startErr == nil {
			break
		}
		if attempt < maxAttempts {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if startErr != nil {
		// Provide a clearer error message for state issues
		if s, ok := status.FromError(startErr); ok && s.Code() == codes.FailedPrecondition {
			return cc.Errorf("VM cannot be started: %s", s.Message())
		}
		return fmt.Errorf("failed to start instance after %d attempts: %w", maxAttempts, startErr)
	}

	// Verify the instance is actually running after start
	verifyResp, err := exeletClient.client.GetInstance(restartCtx, &api.GetInstanceRequest{
		ID: containerID,
	})
	if err != nil {
		return fmt.Errorf("failed to verify instance status after start: %w", err)
	}
	finalState := verifyResp.Instance.State
	if finalState != api.VMState_RUNNING && finalState != api.VMState_STARTING {
		return cc.Errorf("VM failed to start, current state: %s", finalState.String())
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{
			"vm_name": boxName,
			"status":  "restarted",
		})
		return nil
	}
	cc.Writeln("\033[1;32mVM %q restarted successfully\033[0m", boxName)
	// TODO: Block `restart` on changing the `ls` state changing, warning banner for now.
	cc.Writeln("\033[1;32m'ls' may not reflect the correct VM state for a few minutes...\033[0m")
	return nil
}

func (ss *SSHServer) handleDeleteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("please specify at least one VM name to delete")
	}

	var deleted []string
	var failed []string
	seen := make(map[string]bool)

	for _, arg := range cc.Args {
		boxName := ss.normalizeBoxName(arg)
		if seen[boxName] {
			continue
		}
		seen[boxName] = true
		box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
		if err != nil {
			failed = append(failed, boxName)
			cc.WriteError("VM %q not found", boxName)
			continue
		}

		cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)

		if err := ss.server.deleteBox(ctx, box); err != nil {
			failed = append(failed, boxName)
			cc.WriteError("failed to delete %q: %v", boxName, err)
			continue
		}
		deleted = append(deleted, boxName)
	}

	if cc.WantJSON() {
		result := map[string]any{
			"deleted": deleted,
			"failed":  failed,
		}
		cc.WriteJSON(result)
		return nil
	}

	if len(deleted) > 0 {
		cc.Write("\033[1;32m%d VM(s) deleted successfully\033[0m\r\n", len(deleted))
	}
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

func (ss *SSHServer) handleSudoCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	cc.Writeln("")
	cc.Writeln("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	cc.Writeln("")
	cc.Writeln("")
	cc.Writeln("Want to be in the sudoers file? Email david+sudo@bold.dev")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleWhoamiCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	type sshKeyRow struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		Name        string `json:"name,omitempty"`
		Current     bool   `json:"current"`
		id          int64  // unexported; for sorting only
	}
	ccPubKey := strings.TrimSpace(cc.PublicKey)
	dbKeys, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetSSHKeysForUser, cc.User.ID)
	if err != nil {
		return err
	}
	var sshKeys []sshKeyRow
	for _, dbKey := range dbKeys {
		pubKey := strings.TrimSpace(dbKey.PublicKey)
		if pubKey == "" {
			continue
		}
		isCurrent := pubKey == ccPubKey
		// DB stores fingerprint without prefix; add "SHA256:" for display
		fingerprint := "SHA256:" + dbKey.Fingerprint
		sshKeys = append(sshKeys, sshKeyRow{PublicKey: pubKey, Fingerprint: fingerprint, Name: dbKey.Comment, Current: isCurrent, id: dbKey.ID})
	}

	slices.SortFunc(sshKeys, func(a, b sshKeyRow) int {
		if a.Current != b.Current {
			if a.Current {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.id, b.id)
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
		if key.Name != "" {
			cc.Write(" \033[2m(%s)\033[0m", key.Name)
		}
		if key.Current {
			cc.Write(" \033[1;32m← current\033[0m")
		}
		cc.Writeln("")
	}
	return nil
}

func (ss *SSHServer) handleBrowserCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	// Generate a verification token using the same system as email authentication.
	// The verification code for email is anti-phishing, but not needed here since the user directly acquires the link.
	token := generateRegistrationToken()

	// Store verification in database using the existing email verification table
	err := withTx1(ss.server, ctx, (*exedb.Queries).InsertEmailVerification, exedb.InsertEmailVerificationParams{
		Token:        token,
		Email:        cc.User.Email,
		UserID:       cc.User.ID,
		ExpiresAt:    time.Now().Add(15 * time.Minute), // 15 minute expiry
		InviteCodeID: nil,                              // browser command is for existing users, no invite
		IsNewUser:    false,                            // browser command is for existing users
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
	if cc.WantQR() {
		writeQRCode(cc.Output, magicURL)
		cc.Writeln("")
	}
	cc.Writeln("\033[2mExpires in 15 minutes.\033[0m")
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleGrantSupportRootCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: grant-support-root <vmname> on|off")
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

	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found", boxName)
	}
	if err != nil {
		return err
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetBoxSupportAccessAllowed, exedb.SetBoxSupportAccessAllowedParams{
		SupportAccessAllowed: newValue,
		ID:                   box.ID,
	})
	if err != nil {
		return err
	}

	if newValue == 1 {
		cc.Writeln("exe.dev support now has root access to VM %q.", boxName)
	} else {
		cc.Writeln("exe.dev support root access to VM %q has been revoked.", boxName)
	}
	return nil
}

func (ss *SSHServer) handleExeletsCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	type exeletInfo struct {
		Address       string `json:"address"`
		Host          string `json:"host"`
		Version       string `json:"version"`
		Arch          string `json:"arch"`
		Status        string `json:"status"`
		IsPreferred   bool   `json:"is_preferred"`
		InstanceCount int    `json:"instance_count"`
		Error         string `json:"error,omitempty"`
	}

	// Get the preferred exelet setting
	preferredAddr, _ := withRxRes0(ss.server, ctx, (*exedb.Queries).GetPreferredExelet)

	var exelets []exeletInfo

	// Gather info from all exelet clients
	for addr, ec := range ss.server.exeletClients {
		host := addr
		if u, err := url.Parse(addr); err == nil {
			host = u.Hostname()
		}
		info := exeletInfo{
			Address:     addr,
			Host:        host,
			Version:     ec.client.Version(),
			Arch:        ec.client.Arch(),
			IsPreferred: addr == preferredAddr,
		}

		// Try to get system info to verify connectivity
		sysInfoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := ec.client.GetSystemInfo(sysInfoCtx, &api.GetSystemInfoRequest{})
		cancel()
		if err != nil {
			info.Status = "error"
			info.Error = err.Error()
		} else {
			info.Status = "healthy"
			info.Version = resp.Version
			info.Arch = resp.Arch
		}

		// Count instances
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if count, err := ec.countInstances(listCtx); err == nil {
			info.InstanceCount = count
		}
		cancel()

		exelets = append(exelets, info)
	}

	// Sort exelets by address for consistent output
	slices.SortFunc(exelets, func(a, b exeletInfo) int {
		return cmp.Compare(a.Address, b.Address)
	})

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"exelets": exelets,
		})
		return nil
	}

	if len(exelets) == 0 {
		cc.Writeln("No exelets configured.")
		return nil
	}

	for i, e := range exelets {
		cc.Writeln("\033[1m%s\033[0m", e.Host)
		if e.IsPreferred {
			cc.Writeln("  \033[1;33m★ preferred\033[0m")
		}

		statusColor := "\033[1;32m" // green
		if e.Status == "error" {
			statusColor = "\033[1;31m" // red
		}
		cc.Writeln("  %s%s\033[0m (%d instances)", statusColor, e.Status, e.InstanceCount)
		if e.Version != "" || e.Arch != "" {
			cc.Writeln("  Version: %s, Arch: %s", e.Version, e.Arch)
		}
		if e.Error != "" {
			cc.Writeln("  \033[1;31mError:\033[0m %s", e.Error)
		}
		if i < len(exelets)-1 {
			cc.Writeln("")
		}
	}
	return nil
}

func (ss *SSHServer) handleRenameCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: rename <oldname> <newname>")
	}

	oldName := cc.Args[0]
	newName := cc.Args[1]

	// Check if renaming to the same name
	if oldName == newName {
		cc.Write("%s is already named %s\r\n", oldName, newName)
		return nil
	}

	// Validate new name
	if err := boxname.Valid(newName); err != nil {
		return cc.Errorf("invalid new name: %v", err)
	}

	// Check if the box exists and belongs to this user
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            oldName,
		CreatedByUserID: cc.User.ID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cc.Errorf("vm %q not found", oldName)
		}
		return cc.Errorf("failed to look up vm: %v", err)
	}

	// Check if new name already exists (globally)
	exists, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithNameExists, newName)
	if err != nil {
		return cc.Errorf("failed to check name availability: %v", err)
	}
	if exists != 0 {
		return cc.Errorf("name %q already exists", newName)
	}

	if box.ContainerID == nil {
		return cc.Errorf("vm %v not found", box.ContainerID)
	}

	exeletClient := ss.server.getExeletClient(box.Ctrhost)

	if exeletClient == nil {
		return cc.Errorf("internal error")
	}

	instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
		ID: *box.ContainerID,
	})

	if instanceResp == nil || err != nil || instanceResp.Instance.State != api.VMState_RUNNING {
		return cc.Errorf("vm rename not supported when vm is not running!")
	}

	// Update the box name in the database
	slog.InfoContext(ctx, "rename: starting DB update",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"user_id", cc.User.ID)
	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxName, exedb.UpdateBoxNameParams{
		Name:            newName,
		ID:              box.ID,
		CreatedByUserID: cc.User.ID,
	})
	if err != nil {
		slog.ErrorContext(ctx, "rename: DB update failed",
			"box_id", box.ID,
			"old_name", oldName,
			"new_name", newName,
			"error", err)
		return cc.Errorf("failed to rename vm: %v", err)
	}
	slog.InfoContext(ctx, "rename: DB update complete",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"rollback_sql", fmt.Sprintf("UPDATE boxes SET name = '%s' WHERE id = %d", oldName, box.ID))

	// Update the instance name on the exelet so the metadata service returns the new name
	slog.InfoContext(ctx, "rename: updating exelet instance config",
		"box_id", box.ID,
		"container_id", *box.ContainerID,
		"new_name", newName)
	_, err = exeletClient.client.RenameInstance(ctx, &api.RenameInstanceRequest{
		ID:   *box.ContainerID,
		Name: newName,
	})
	if err != nil {
		slog.ErrorContext(ctx, "rename: failed to update exelet instance config",
			"box_id", box.ID,
			"container_id", *box.ContainerID,
			"new_name", newName,
			"error", err)
		// Continue despite error - the DB rename succeeded, and the metadata service
		// will return the old name until the VM is restarted, which is not ideal but
		// not critical enough to roll back the entire rename.
	} else {
		slog.InfoContext(ctx, "rename: exelet instance config updated",
			"box_id", box.ID,
			"container_id", *box.ContainerID,
			"new_name", newName)
	}

	// Get the IP shard for DNS record update (need this early to create new DNS record)
	ipShard, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIPShardByBoxName, newName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.ErrorContext(ctx, "rename: failed to get IP shard for DNS - manual DNS check required",
			"box_id", box.ID,
			"old_name", oldName,
			"new_name", newName,
			"error", err)
	}

	// Invalidate all auth cookies for the old box name to prevent cookie hijacking.
	// When a box is renamed, any cookies issued for the old name's domain (e.g., oldname.exe.dev)
	// should be invalidated so that if someone else creates a box with the old name, they
	// cannot use any lingering cookies from the original owner.
	for _, oldDomain := range []string{
		ss.server.env.BoxSub(oldName),        // oldname.exe.cloud
		ss.server.env.BoxXtermSub(oldName),   // oldname.xterm.exe.cloud
		ss.server.env.BoxShelleySub(oldName), // oldname.shelley.exe.cloud
	} {
		if err := withTx1(ss.server, ctx, (*exedb.Queries).DeleteAuthCookiesByDomain, oldDomain); err != nil {
			slog.ErrorContext(ctx, "rename: failed to invalidate auth cookies for old box name",
				"box_id", box.ID,
				"old_name", oldName,
				"old_domain", oldDomain,
				"error", err)
		}
	}

	// Update hostname inside the running VM
	// Re-fetch the box with the new name for SSH connection
	slog.InfoContext(ctx, "rename: updating hostname inside VM",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName)
	updatedBox, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            newName,
		CreatedByUserID: cc.User.ID,
	})
	if err == nil {
		// Update /etc/hostname and /etc/hosts
		// Use busybox shell and sed to replace old hostname with new hostname
		// We use /exe.dev/bin/sh (busybox) to ensure sed/hostname are available even on minimal images
		hostnameCmd := fmt.Sprintf(
			"sudo /exe.dev/bin/sh -c 'sed -i \"s/\\b%s\\b/%s/g\" /etc/hostname /etc/hosts 2>/dev/null; hostname %s 2>/dev/null'",
			oldName, newName, newName,
		)
		if _, err := runCommandOnBox(ctx, ss.server.sshPool, &updatedBox, hostnameCmd); err != nil {
			slog.ErrorContext(ctx, "rename: failed to update hostname inside VM",
				"box_id", box.ID,
				"old_name", oldName,
				"new_name", newName,
				"error", err)
		} else {
			slog.InfoContext(ctx, "rename: hostname update inside VM complete",
				"box_id", box.ID,
				"new_name", newName)
		}

		// Update /exe.dev/shelley.json with the new box name
		shelleyConf, err := ss.makeShelleyConfig(newName)
		if err != nil {
			slog.ErrorContext(ctx, "rename: failed to generate shelley.json",
				"box_id", box.ID,
				"new_name", newName,
				"error", err)
		} else {
			shelleyCmd := fmt.Sprintf("sudo /exe.dev/bin/sh -c 'cat > /exe.dev/shelley.json << \"SHELLEY_EOF\"\n%s\nSHELLEY_EOF'", shelleyConf)
			if _, err := runCommandOnBox(ctx, ss.server.sshPool, &updatedBox, shelleyCmd); err != nil {
				slog.ErrorContext(ctx, "rename: failed to update shelley.json inside VM",
					"box_id", box.ID,
					"new_name", newName,
					"error", err)
			} else {
				slog.InfoContext(ctx, "rename: shelley.json update inside VM complete",
					"box_id", box.ID,
					"new_name", newName)
			}
		}
	} else {
		slog.ErrorContext(ctx, "rename: failed to re-fetch box for hostname update",
			"box_id", box.ID,
			"new_name", newName,
			"error", err)
	}

	slog.InfoContext(ctx, "rename: completed successfully",
		"box_id", box.ID,
		"old_name", oldName,
		"new_name", newName,
		"user_id", cc.User.ID,
		"ip_shard", ipShard)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{
			"old_name": oldName,
			"new_name": newName,
			"status":   "renamed",
		})
		return nil
	}
	cc.Write("Renamed %s to %s\r\n", oldName, newName)
	return nil
}

// runCommandOnBox executes a command on a box via SSH and returns the combined output.
func runCommandOnBox(ctx context.Context, pool *sshpool2.Pool, box *exedb.Box, command string) ([]byte, error) {
	return runCommandOnBoxWithStdin(ctx, pool, box, command, nil)
}

// runCommandOnBoxWithStdin executes a command on a box via SSH with optional stdin.
func runCommandOnBoxWithStdin(ctx context.Context, pool *sshpool2.Pool, box *exedb.Box, command string, stdin io.Reader) ([]byte, error) {
	if box.SSHPort == nil || box.SSHUser == nil || len(box.SSHClientPrivateKey) == 0 {
		return nil, fmt.Errorf("box %q does not have SSH configured", box.Name)
	}

	sshSigner, err := ssh.ParsePrivateKey(box.SSHClientPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH key: %w", err)
	}

	sshHost := box.SSHHost()
	sshConfig := &ssh.ClientConfig{
		User:            *box.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshSigner)},
		HostKeyCallback: box.CreateHostKeyCallback(),
		Timeout:         10 * time.Second,
	}

	connRetries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	return pool.RunCommand(ctx, sshHost, *box.SSHUser, int(*box.SSHPort), sshSigner, sshConfig, command, stdin, connRetries)
}

// scpToBox copies content to a remote file on a box via SSH.
// It writes to a temp file in /tmp, then uses sudo mv to move it to the final destination.
// remotePath must be an absolute path.
func scpToBox(ctx context.Context, pool *sshpool2.Pool, box *exedb.Box, content io.Reader, remotePath string, mode os.FileMode) error {
	// Write to temp file, then sudo mv to final destination
	tmpPath := fmt.Sprintf("/tmp/scp.%s", crand.Text()) // doesn't need quoting
	quotedDest, err := syntax.Quote(remotePath, syntax.LangBash)
	if err != nil {
		// exceedingly unlikely, but check anyway
		return fmt.Errorf("failed to quote remote path: %w", err)
	}

	// On failure, remove temp file.
	cleanup := func() {
		// Use a fresh context so cleanup runs even if the original context was canceled.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runCommandOnBox(cleanupCtx, pool, box, fmt.Sprintf("rm -f %s", tmpPath))
	}

	// Write content to temp file
	writeCmd := fmt.Sprintf("cat > %s && chmod %04o %s", tmpPath, mode, tmpPath)
	if output, err := runCommandOnBoxWithStdin(ctx, pool, box, writeCmd, content); err != nil {
		cleanup()
		return fmt.Errorf("write to temp file failed: cmd=%q output=%q: %w", writeCmd, output, err)
	}

	// Move to final destination with sudo
	mvCmd := fmt.Sprintf("sudo mv %s %s", tmpPath, quotedDest)
	if output, err := runCommandOnBox(ctx, pool, box, mvCmd); err != nil {
		cleanup()
		return fmt.Errorf("move to destination failed: cmd=%q output=%q: %w", mvCmd, output, err)
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

	boxes, err := withRxRes1(ss.server, context.Background(), (*exedb.Queries).BoxesForUser, cc.User.ID)
	if err != nil {
		return nil
	}

	alreadySelected := make(map[string]bool)
	for _, arg := range cc.Args {
		alreadySelected[ss.normalizeBoxName(arg)] = true
	}

	var completions []string
	prefix := compCtx.CurrentWord
	for _, box := range boxes {
		if alreadySelected[box.Name] {
			continue
		}
		if strings.HasPrefix(box.Name, prefix) {
			completions = append(completions, box.Name)
		}
	}
	return completions
}

// completeRenameArgs completes the first argument (oldname) with existing box names,
// but does not complete the second argument (newname) since that should be a new name.
func (ss *SSHServer) completeRenameArgs(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	// Only complete the first positional argument (the old name)
	if compCtx.Position != 1 {
		return nil
	}
	return ss.completeBoxNames(compCtx, cc)
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

func (ss *SSHServer) completeCommandNames(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if ss == nil || ss.commands == nil {
		return nil
	}
	prefix := compCtx.CurrentWord
	var completions []string
	for _, cmd := range ss.commands.GetAvailableCommands(cc) {
		if cmd.Hidden && !ss.commands.DevMode {
			continue
		}
		if strings.HasPrefix(cmd.Name, prefix) {
			completions = append(completions, cmd.Name)
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
		return cc.Errorf("usage: ssh <vmname> [command...]")
	}

	name := cc.Args[0]
	cmdArgs := cc.Args[1:]

	// Trim the @host if present and validate it
	if _, found := strings.CutPrefix(name, "@"); found {
		// If they typed just @host with no boxname
		return cc.Errorf("usage: ssh <vmname> [command...]")
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
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            name,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found", name)
	}
	if err != nil {
		return fmt.Errorf("failed to look up VM: %w", err)
	}

	// Validate box has SSH credentials
	if box.SSHPort == nil || box.SSHUser == nil || len(box.SSHClientPrivateKey) == 0 {
		return cc.Errorf("VM %q does not have SSH configured", name)
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
		HostKeyCallback: box.CreateHostKeyCallback(),
		Timeout:         10 * time.Second,
	}

	// Connect to the box with context support using net.Dialer
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", sshAddr)
	if err != nil {
		return cc.Errorf("failed to connect to VM: %v", err)
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
		if errorz.HasType[*ssh.ExitError](err) {
			// Return nil since we already wrote output; exit code is informational
			return nil
		}
		if err != nil {
			return cc.Errorf("command failed: %v", err)
		}
	} else {
		// Interactive mode - wire up stdin for the shell
		session.Stdin = cc.SSHSession

		// Get PTY info from the client session and set it up first
		pty, _ := cc.SSHSession.Pty()
		if err := session.RequestPty(
			// TODO(bmizerany): get actual terminal type from client (or env)? good enough for now
			"xterm-256color",

			pty.Window.Height,
			pty.Window.Width,
			nil,
		); err != nil {
			return cc.Errorf("failed to request PTY: %v", err)
		}

		// Forward window size changes to the remote session.
		go func() {
			for cc.SSHSession.WaitWindowChange() {
				pty, _ := cc.SSHSession.Pty()
				session.WindowChange(pty.Window.Height, pty.Window.Width)
			}
		}()

		// Interactive mode - start shell
		if err := session.Shell(); err != nil {
			return cc.Errorf("failed to start shell: %v", err)
		}
		session.Wait()
	}
	return nil
}

const (
	maxDiskGrowth = 250 * 1024 * 1024 * 1024 // 250GB max growth in a single operation
)

func (ss *SSHServer) handleResizeCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	// Only support users can use this command
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>]\nMemory/disk are in GB (e.g., '8' for 8GB). CPU is the number of vCPUs. Disk can only be grown, not shrunk.")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
	memoryStr := cc.FlagSet.Lookup("memory").Value.String()
	cpuVal, _ := strconv.ParseUint(cc.FlagSet.Lookup("cpu").Value.String(), 10, 64)
	diskStr := cc.FlagSet.Lookup("disk").Value.String()

	// Validate at least one option is specified
	if memoryStr == "" && cpuVal == 0 && diskStr == "" {
		return cc.Errorf("usage: resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>]\nAt least one of --memory, --cpu, or --disk must be specified.")
	}

	// Look up the box by name (support users can look up any box)
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found", boxName)
	}
	if err != nil {
		return cc.Errorf("failed to look up VM: %v", err)
	}

	if box.ContainerID == nil || *box.ContainerID == "" {
		return cc.Errorf("VM %q has no container ID", boxName)
	}

	// Get the exelet client for this box's container host
	exeletClient := ss.server.getExeletClient(box.Ctrhost)
	if exeletClient == nil {
		return cc.Errorf("no exelet client available for host %s", box.Ctrhost)
	}

	// Handle disk resize if specified
	var diskGrowResult *api.GrowDiskResponse
	if diskStr != "" {
		newDiskSize, err := parseSize(diskStr)
		if err != nil {
			return cc.Errorf("invalid --disk value: %s", err)
		}

		// Get current instance to check current disk size
		instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
			ID: *box.ContainerID,
		})
		if err != nil {
			if st, ok := status.FromError(err); ok {
				return cc.Errorf("failed to get instance info: %s", st.Message())
			}
			return cc.Errorf("failed to get instance info: %v", err)
		}

		currentDiskSize := instanceResp.Instance.VMConfig.Disk
		if newDiskSize <= currentDiskSize {
			return cc.Errorf("--disk must be larger than current size (%s)", humanize.IBytes(currentDiskSize))
		}

		additionalBytes := newDiskSize - currentDiskSize
		if additionalBytes > maxDiskGrowth {
			return cc.Errorf("disk growth cannot exceed %s in a single operation", humanize.IBytes(maxDiskGrowth))
		}

		if !cc.WantJSON() {
			cc.Writeln("Growing disk to %s...", humanize.IBytes(newDiskSize))
		}

		diskGrowResult, err = exeletClient.client.GrowDisk(ctx, &api.GrowDiskRequest{
			ID:              *box.ContainerID,
			AdditionalBytes: additionalBytes,
		})
		if err != nil {
			if st, ok := status.FromError(err); ok {
				return cc.Errorf("failed to grow disk: %s", st.Message())
			}
			return cc.Errorf("failed to grow disk: %v", err)
		}
	}

	// Handle memory/CPU resize if specified
	var resizeResult *api.ResizeVMResponse
	if memoryStr != "" || cpuVal > 0 {
		req := &api.ResizeVMRequest{
			ID: *box.ContainerID,
		}

		// Parse and validate memory if specified
		if memoryStr != "" {
			memoryBytes, err := parseSize(memoryStr)
			if err != nil {
				return cc.Errorf("invalid --memory value: %s", err)
			}
			if memoryBytes < stage.MinMemory {
				return cc.Errorf("--memory must be at least %s", humanize.Bytes(stage.MinMemory))
			}
			if memoryBytes > stage.SupportMaxMemory {
				return cc.Errorf("--memory cannot exceed %s", humanize.Bytes(stage.SupportMaxMemory))
			}
			req.Memory = &memoryBytes
		}

		// Validate CPU if specified
		if cpuVal > 0 {
			if cpuVal < stage.MinCPUs {
				return cc.Errorf("--cpu must be at least %d", stage.MinCPUs)
			}
			if cpuVal > stage.SupportMaxCPUs {
				return cc.Errorf("--cpu cannot exceed %d", stage.SupportMaxCPUs)
			}
			req.CPUs = &cpuVal
		}

		if !cc.WantJSON() {
			var changes []string
			if req.Memory != nil {
				changes = append(changes, fmt.Sprintf("memory to %s", humanize.Bytes(*req.Memory)))
			}
			if req.CPUs != nil {
				changes = append(changes, fmt.Sprintf("CPU to %d", *req.CPUs))
			}
			cc.Writeln("Resizing %s...", strings.Join(changes, " and "))
		}

		// Call the exelet to resize the VM
		resizeResult, err = exeletClient.client.ResizeVM(ctx, req)
		if err != nil {
			if st, ok := status.FromError(err); ok {
				return cc.Errorf("failed to resize VM: %s", st.Message())
			}
			return cc.Errorf("failed to resize VM: %v", err)
		}
	}

	// Build result
	if cc.WantJSON() {
		result := map[string]any{
			"vm_name": boxName,
		}
		if resizeResult != nil {
			result["old_memory"] = resizeResult.OldMemory
			result["new_memory"] = resizeResult.NewMemory
			result["old_cpus"] = resizeResult.OldCPUs
			result["new_cpus"] = resizeResult.NewCPUs
		}
		if diskGrowResult != nil {
			result["disk_old_bytes"] = diskGrowResult.OldSize
			result["disk_new_bytes"] = diskGrowResult.NewSize
		}
		cc.WriteJSON(result)
		return nil
	}

	// Human-readable output
	if resizeResult != nil {
		if resizeResult.OldMemory != resizeResult.NewMemory {
			cc.Writeln("Memory: %s -> %s", humanize.IBytes(resizeResult.OldMemory), humanize.IBytes(resizeResult.NewMemory))
		}
		if resizeResult.OldCPUs != resizeResult.NewCPUs {
			cc.Writeln("CPUs: %d -> %d", resizeResult.OldCPUs, resizeResult.NewCPUs)
		}
	}

	if diskGrowResult != nil {
		cc.Writeln("Disk: %s -> %s", humanize.IBytes(diskGrowResult.OldSize), humanize.IBytes(diskGrowResult.NewSize))
	}

	cc.Writeln("")
	cc.Writeln("Configuration updated. Restart the VM to apply changes:")
	cc.Writeln("  %s restart %s", ss.server.replSSHConnectionCommand(), boxName)
	if diskGrowResult != nil {
		cc.Writeln("After restart, run resize2fs inside the VM:")
		cc.Writeln("  ssh %s sudo resize2fs /dev/vda", ss.server.env.BoxDest(boxName))
	}

	return nil
}
