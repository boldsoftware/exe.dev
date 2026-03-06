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
	"path"
	"regexp"
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
	"exe.dev/exeweb"
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

const shelleyDefaultModel = "claude-opus-4.6"

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
func tagCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	fs.Bool("json", false, "output in JSON format")
	fs.Bool("d", false, "delete tag")
	return fs
}

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
	fs.String("prompt", "", "initial prompt to send to Shelley after VM creation (requires exeuntu image); use /dev/stdin to read from stdin")
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

// cpCommandFlags creates a FlagSet for the cp command
func cpCommandFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	fs.Bool("json", false, "output in JSON format")
	// Resource allocation flags - copy uses source values if not specified
	fs.String("memory", "", "[hidden] memory allocation (e.g., 4, 4GB, 8G)")
	fs.String("disk", "", "[hidden] disk size (e.g., 20, 20GB, 50G)")
	fs.Uint("cpu", 0, "[hidden] number of CPUs")
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
			Name:          "help",
			Description:   "Show help information",
			Handler:       ss.handleHelpCommand,
			FlagSetFunc:   jsonOnlyFlags("help"), // not used at runtime (RawArgs), but picked up by doc generator
			RawArgs:       true,
			CompleterFunc: ss.completeCommandNames,
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
				"echo 'build me a web app' | ssh exe.dev new --prompt=/dev/stdin",
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
			Name:              "tag",
			Description:       "Add or remove a tag on a VM",
			Usage:             "tag [-d] <vm> <tag-name>",
			FlagSetFunc:       tagCommandFlags,
			HasPositionalArgs: true,
			CompleterFunc:     ss.completeBoxNames,
			Handler:           ss.handleTagCommand,
			Examples: []string{
				"tag my-vm prod        # add tag",
				"tag -d my-vm prod     # remove tag",
			},
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
		ss.integrationsCommand(),
		ss.teamCommand(),
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
			Usage:             "ssh [-l user] [user@]vmname [command...]",
			Handler:           ss.handleSSHCommand,
			HasPositionalArgs: true,
			RawArgs:           true,
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
			Name:         "exelets",
			Hidden:       true,
			RequiresSudo: true,
			Description:  "List all exelets (support only)",
			FlagSetFunc:  jsonOnlyFlags("exelets"),
			Handler:      ss.handleExeletsCommand,
		},
		{
			Name:              "resize",
			Hidden:            true,
			RequiresSudo:      true,
			Description:       "Resize a VM's memory, CPU, or disk (support only)",
			Usage:             "resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>]",
			HasPositionalArgs: true,
			FlagSetFunc:       resizeCommandFlags,
			Handler:           ss.handleResizeCommand,
		},
		{
			Name:              "backfill-allocated-cpus",
			Hidden:            true,
			RequiresSudo:      true,
			Description:       "Backfill allocated_cpus from exelet for VMs missing it (support only)",
			Usage:             "backfill-allocated-cpus <count>",
			HasPositionalArgs: true,
			Handler:           ss.handleBackfillAllocatedCPUs,
		},
		{
			Name:              "throttle-vm",
			Hidden:            true,
			Description:       "Manage cgroup overrides for a VM (support only)",
			Usage:             "throttle-vm <vmname> [--cpu=<fraction>] [--io=<bw>] [--io-read=<bw>] [--io-write=<bw>] [--raw=<path:value>] [--show] [--clear]",
			HasPositionalArgs: true,
			FlagSetFunc:       throttleVMFlags,
			CompleterFunc:     ss.completeBoxNames,
			Handler:           ss.handleThrottleVMCommand,
		},
		{
			Name:              "throttle-user",
			Hidden:            true,
			Description:       "Manage cgroup overrides for a user (support only)",
			Usage:             "throttle-user <email-or-userid> [--cpu=<fraction>] [--io=<bw>] [--io-read=<bw>] [--io-write=<bw>] [--raw=<path:value>] [--show] [--clear]",
			HasPositionalArgs: true,
			FlagSetFunc:       throttleUserFlags,
			Handler:           ss.handleThrottleUserCommand,
		},
		{
			Name:         "sudo-exe",
			Hidden:       true,
			RequiresSudo: true,
			Description:  "Administrative commands (support only)",
			Subcommands: []*exemenu.Command{
				{
					Name:              "llm-credits",
					Hidden:            true,
					RequiresSudo:      true,
					Description:       "Show LLM credit state for a user",
					Usage:             "sudo-exe llm-credits <userid-or-email>",
					HasPositionalArgs: true,
					FlagSetFunc:       jsonOnlyFlags("llm-credits"),
					Handler:           ss.handleLLMCreditsCommand,
				},
				{
					Name:              "set-limits",
					Hidden:            true,
					RequiresSudo:      true,
					Description:       "Set resource limit overrides for a user (support only)",
					Usage:             "sudo-exe set-limits <userid-or-email> [--max-boxes=N] [--max-memory=SIZE] [--max-disk=SIZE] [--max-cpus=N] [--show] [--clear]",
					HasPositionalArgs: true,
					FlagSetFunc:       setLimitsFlags,
					Handler:           ss.handleSetLimitsCommand,
				},
			},
			Handler: func(ctx context.Context, cc *exemenu.CommandContext) error {
				return cc.Errorf("usage: sudo-exe <subcommand>")
			},
		},
		{
			Name:              "exe0-to-exe1",
			Hidden:            true,
			Description:       "Trade an exe0 token for a shorter exe1 token",
			Usage:             "exe0-to-exe1 [--vm=VMNAME] <exe0-token>",
			HasPositionalArgs: true,
			FlagSetFunc:       exe0ToExe1Flags,
			Handler:           ss.handleExe0ToExe1Command,
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
		ct.SudoChecker = ss.server.UserHasExeSudo
	}
	return ct
}

func (ss *SSHServer) handleHelpCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if cc.User != nil {
		ss.server.recordUserEventBestEffort(ctx, cc.User.ID, userEventHasRunHelp)
	}

	// RawArgs mode: manually extract --json and filter flags out of the
	// command path so that "help integrations add --header foo" finds
	// "integrations add" instead of failing on unknown flags.
	var cmdPath []string
	for _, a := range cc.Args {
		if a == "--json" {
			cc.ForceJSON = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		cmdPath = append(cmdPath, a)
	}

	if len(cmdPath) > 0 {
		// Help for specific command
		cmd := ss.commands.FindCommand(cmdPath)
		if cmd == nil {
			if cc.WantJSON() {
				return cc.Errorf("no help available for unrecognized command: %s", strings.Join(cmdPath, " "))
			}
			cc.Writeln("No help available for unrecognized command: %s", strings.Join(cmdPath, " "))
			return nil
		}

		return cmd.Help(cc)
	}

	if cc.WantJSON() {
		ss.commands.HelpJSON(cc)
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
					if re.MatchString(b.Name) || slices.ContainsFunc(b.GetTags(), func(t string) bool { return re.MatchString(t) }) {
						filtered = append(filtered, b)
					}
				}
			} else {
				// Literal name or tag
				name := ss.normalizeBoxName(arg)
				for _, b := range boxes {
					if b.Name == name || slices.Contains(b.GetTags(), arg) {
						filtered = append(filtered, b)
					}
				}
			}
		}
		slices.SortFunc(filtered, func(a, b exedb.Box) int { return cmp.Compare(a.Name, b.Name) })
		boxes = slices.CompactFunc(filtered, func(a, b exedb.Box) bool { return a.Name == b.Name })
	}

	if cc.WantJSON() {
		vmList := []map[string]any{}
		for _, vm := range boxes {
			status := container.ContainerStatus(vm.Status).String()
			box := map[string]any{
				"vm_name":   vm.Name,
				"ssh_dest":  ss.server.env.BoxDest(vm.Name),
				"status":    status,
				"region":    vm.Region,
				"https_url": ss.server.boxProxyAddress(vm.Name),
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
			if strings.Contains(vm.Image, "exeuntu") {
				box["shelley_url"] = ss.server.shelleyURL(vm.Name)
			}
			if tags := vm.GetTags(); len(tags) > 0 {
				box["tags"] = tags
			}
			vmList = append(vmList, box)
		}

		// Include team VMs for team owners
		teamBoxes, _ := ss.server.ListTeamBoxesForSudoer(ctx, cc.User.ID)
		var teamVMList []map[string]any
		for _, vm := range teamBoxes {
			status := container.ContainerStatus(vm.Status).String()
			box := map[string]any{
				"vm_name":       vm.Name,
				"ssh_dest":      ss.server.env.BoxDest(vm.Name),
				"status":        status,
				"region":        vm.Region,
				"https_url":     ss.server.boxProxyAddress(vm.Name),
				"creator_email": vm.CreatorEmail,
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
			if strings.Contains(vm.Image, "exeuntu") {
				box["shelley_url"] = ss.server.shelleyURL(vm.Name)
			}
			if tags := parseTags(vm.Tags); len(tags) > 0 {
				box["tags"] = tags
			}
			teamVMList = append(teamVMList, box)
		}

		result := map[string]any{"vms": vmList}
		if len(teamVMList) > 0 {
			result["team_vms"] = teamVMList
		}
		cc.WriteJSON(result)
		return nil
	}

	// Check if user is a team owner and get team boxes (need this early to decide output)
	teamBoxes, _ := ss.server.ListTeamBoxesForSudoer(ctx, cc.User.ID)

	if len(boxes) == 0 && len(teamBoxes) == 0 {
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
			tagStr := ""
			if tags := b.GetTags(); len(tags) > 0 {
				for _, t := range tags {
					tagStr += " #" + t
				}
			}
			fmt.Fprintf(tw, "\033[1m%s\033[0m\t%s%s\033[0m\t%s\t%s\t%s\t\033[36m%s\033[0m\r\n",
				ss.server.env.BoxSub(b.Name),
				statusColor(status), status,
				b.Region,
				shelleyURL,
				ss.server.boxProxyAddress(b.Name),
				tagStr,
			)
		}
		tw.Flush()
		return nil
	}

	// Show user's own VMs
	if len(boxes) > 0 {
		cc.Write("\033[1;36mYour VMs:\033[0m\r\n")
	}
	for _, b := range boxes {
		status := container.ContainerStatus(b.Status)
		cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", ss.server.env.BoxSub(b.Name), statusColor(status), status)
		imageName := container.GetDisplayImageName(b.Image)
		switch imageName {
		case "exeuntu", "":
		default:
			cc.Write(" (%s)", imageName)
		}
		if tags := b.GetTags(); len(tags) > 0 {
			for _, t := range tags {
				cc.Write(" \033[36m#%s\033[0m", t)
			}
		}
		cc.Write("\r\n")
	}

	// Show team VMs for team owners
	if len(teamBoxes) > 0 {
		cc.Write("\r\n\033[1;33mTeam VMs:\033[0m\r\n")
		for _, b := range teamBoxes {
			status := container.ContainerStatus(b.Status)
			cc.Write("  • \033[1m%s\033[0m - %s%s\033[0m", ss.server.env.BoxSub(b.Name), statusColor(status), status)
			imageName := container.GetDisplayImageName(b.Image)
			switch imageName {
			case "exeuntu", "":
			default:
				cc.Write(" (%s)", imageName)
			}
			if tags := parseTags(b.Tags); len(tags) > 0 {
				for _, t := range tags {
					cc.Write(" \033[36m#%s\033[0m", t)
				}
			}
			cc.Write(" \033[90mby %s\033[0m", b.CreatorEmail)
			cc.Write("\r\n")
		}
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

	// Verify the box exists and user has access (owner or team owner)
	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, boxName)
	if err != nil {
		return cc.Errorf("VM %q not found", boxName)
	}

	CommandLogAddAttr(ctx, slog.String("vm_name", boxName))
	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))
	CommandLogAddAttr(ctx, slog.String("vm_owner_user_id", box.CreatedByUserID))

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
			ss.server.sshPool.DropConnectionsTo(exeweb.BoxSSHHost(ss.server.slog(), box.Ctrhost), int(*box.SSHPort))
		}
	case api.VMState_STOPPED, api.VMState_ERROR, api.VMState_CREATED:
		// Instance is already stopped or in a restartable state, skip stop.
		// But still drop any pooled SSH connections - they may be stale from
		// before the VM was stopped (e.g., poweroff from inside the VM).
		if box.SSHPort != nil {
			ss.server.sshPool.DropConnectionsTo(exeweb.BoxSSHHost(ss.server.slog(), box.Ctrhost), int(*box.SSHPort))
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

	// Sync SSH port from exelet if the DB doesn't have one
	// (e.g. after migrating a stopped instance, the exelet allocates a new port on start).
	if box.SSHPort == nil && verifyResp.Instance != nil && verifyResp.Instance.SSHPort != 0 {
		newSSHPort := int64(verifyResp.Instance.SSHPort)
		if err := withTx1(ss.server, restartCtx, (*exedb.Queries).UpdateBoxSSHPort, exedb.UpdateBoxSSHPortParams{
			SSHPort: &newSSHPort,
			ID:      box.ID,
		}); err != nil {
			ss.server.slog().ErrorContext(restartCtx, "failed to update SSH port after restart", "box", boxName, "error", err)
		}
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{
			"vm_name": boxName,
			"status":  "restarted",
		})
		return nil
	}
	cc.Writeln("\033[1;32mVM %q restarted successfully\033[0m", boxName)
	return nil
}

func (ss *SSHServer) handleDeleteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("please specify at least one VM name to delete")
	}

	deleted := []string{}
	failed := []string{}
	seen := make(map[string]bool)

	for _, arg := range cc.Args {
		boxName := ss.normalizeBoxName(arg)
		if seen[boxName] {
			continue
		}
		seen[boxName] = true
		box, accessType, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, boxName)
		if err != nil {
			failed = append(failed, boxName)
			cc.WriteError("VM %q not found", boxName)
			continue
		}

		if accessType == TeamBoxAccessTeamSudoer {
			cc.Writeln("Deleting team VM \033[1m%s\033[0m...", boxName)
		} else {
			cc.Writeln("Deleting \033[1m%s\033[0m...", boxName)
		}

		if err := ss.server.deleteBox(ctx, *box); err != nil {
			failed = append(failed, boxName)
			cc.WriteError("failed to delete %q: %v", boxName, err)
			continue
		}
		deleted = append(deleted, boxName)
	}

	if len(deleted) > 0 {
		CommandLogAddAttr(ctx, slog.String("vm_name", strings.Join(deleted, ",")))
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
	sshKeys := []sshKeyRow{}
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

	exelets := []exeletInfo{}

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

	sshHost := exeweb.BoxSSHHost(slog.Default(), box.Ctrhost)
	sshConfig := &ssh.ClientConfig{
		User:            *box.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshSigner)},
		HostKeyCallback: exeweb.CreateHostKeyCallback(box.Name, box.SSHServerIdentityKey),
		Timeout:         10 * time.Second,
	}

	connRetries := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	return pool.RunCommand(ctx, sshHost, *box.SSHUser, int(*box.SSHPort), sshSigner, sshConfig, command, stdin, connRetries)
}

// scpToBox copies content to a remote file on a box via SSH.
// It writes to a temp file, then uses sudo mv to move it to the final destination.
// remotePath must be an absolute path.
func scpToBox(ctx context.Context, pool *sshpool2.Pool, box *exedb.Box, content io.Reader, remotePath string, mode os.FileMode) error {
	// We use a .exe-tmp dir in the *parent* of the destination directory.
	// Three constraints drive this:
	//   1. Must be on the same volume as the destination so mv is atomic.
	//   2. Must not be /tmp, which systemd-tmpfiles-setup cleans on boot,
	//      racing with in-flight copies. See boldsoftware/exe.dev#147.
	//   3. Must not be inside the destination directory itself, because
	//      mkdir/rmdir would generate spurious inotify events that
	//      interfere with user inotifywait scripts (e.g. email delivery).
	// The parent of the destination directory satisfies all three.
	destDir := path.Dir(remotePath)
	tmpDir := path.Dir(destDir) + "/.exe-tmp"
	tmpFile := fmt.Sprintf("%s/scp.%s", tmpDir, crand.Text())
	quotedDest, err := syntax.Quote(remotePath, syntax.LangBash)
	if err != nil {
		return fmt.Errorf("failed to quote remote path: %w", err)
	}
	// tmpDir and tmpFile are quote-safe by construction (crand.Text is alphanumeric).

	// Create .exe-tmp dir (sudo in case parent dir is owned by root, e.g. /usr/local).
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chmod 1777 %s", tmpDir, tmpDir)
	if output, err := runCommandOnBox(ctx, pool, box, mkdirCmd); err != nil {
		return fmt.Errorf("failed to create temp dir: cmd=%q output=%q: %w", mkdirCmd, output, err)
	}

	// On failure, remove temp file.
	cleanup := func() {
		// Use a fresh context so cleanup runs even if the original context was canceled.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runCommandOnBox(cleanupCtx, pool, box, fmt.Sprintf("rm -f %s", tmpFile))
	}

	// Write content to temp file
	writeCmd := fmt.Sprintf("cat > %s && chmod %04o %s", tmpFile, mode, tmpFile)
	if output, err := runCommandOnBoxWithStdin(ctx, pool, box, writeCmd, content); err != nil {
		cleanup()
		return fmt.Errorf("write to temp file failed: cmd=%q output=%q: %w", writeCmd, output, err)
	}

	// Move to final destination with sudo
	mvCmd := fmt.Sprintf("sudo mv %s %s", tmpFile, quotedDest)
	if output, err := runCommandOnBox(ctx, pool, box, mvCmd); err != nil {
		cleanup()
		return fmt.Errorf("move to destination failed: cmd=%q output=%q: %w", mvCmd, output, err)
	}

	// Best-effort cleanup of the temp dir. Fails harmlessly if another scp is in flight.
	runCommandOnBox(ctx, pool, box, fmt.Sprintf("sudo rmdir %s 2>/dev/null", tmpDir))
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
	if cc.SSHSession == nil {
		return cc.Errorf("ssh command requires an SSH session")
	}

	args := cc.Args
	if len(args) == 0 {
		return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
	}

	sshUser := ""
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "-l":
			if len(args) < 2 {
				return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
			}
			sshUser = args[1]
			args = args[2:]
		case strings.HasPrefix(arg, "-l"):
			// Support -lroot as shorthand for -l root.
			if len(arg) == 2 {
				return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
			}
			sshUser = arg[2:]
			args = args[1:]
		default:
			goto doneParsingSSHFlags
		}
	}

doneParsingSSHFlags:
	if len(args) < 1 {
		return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
	}

	name := args[0]
	cmdArgs := args[1:]

	// Trim the @host if present and validate it. Also support user@vmname.
	if _, found := strings.CutPrefix(name, "@"); found {
		// If they typed just @host with no boxname
		return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
	} else if lhs, rhs, found := strings.Cut(name, "@"); found {
		// Format: user@boxname, boxname@host, or user@boxname.host.
		normalized := ss.normalizeBoxName(rhs)
		if normalized != rhs {
			// user@boxname.host
			if lhs == "" {
				return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
			}
			if sshUser == "" {
				sshUser = lhs
			}
			name = normalized
		} else if rhs == ss.server.env.BoxHost {
			// boxname@host
			name = lhs
		} else {
			// user@boxname
			if lhs == "" {
				return cc.Errorf("usage: ssh [-l user] [user@]vmname [command...]")
			}
			if sshUser == "" {
				sshUser = lhs
			}
			name = rhs
		}
	}

	// Also handle boxname.host format (e.g., "connx.exe.xyz")
	name = ss.normalizeBoxName(name)

	// Look up the box (owner or team owner access)
	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, name)
	if err != nil {
		return cc.Errorf("VM %q not found", name)
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

	boxSSHUser := *box.SSHUser
	if sshUser != "" {
		boxSSHUser = sshUser
	}

	sshHost := exeweb.BoxSSHHost(ss.server.slog(), box.Ctrhost)
	sshAddr := fmt.Sprintf("%s:%d", sshHost, *box.SSHPort)
	slog.InfoContext(ctx, "ssh command connecting to box", "addr", sshAddr, "user", boxSSHUser, "ctrhost", box.Ctrhost)

	sshConfig := &ssh.ClientConfig{
		User: boxSSHUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshSigner),
		},
		HostKeyCallback: exeweb.CreateHostKeyCallback(box.Name, box.SSHServerIdentityKey),
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

	CommandLogAddAttr(ctx, slog.String("vm_name", boxName))
	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))
	CommandLogAddAttr(ctx, slog.String("vm_owner_user_id", box.CreatedByUserID))

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

		// Update allocated_cpus in DB if CPUs changed
		if req.CPUs != nil && resizeResult.NewCPUs != resizeResult.OldCPUs {
			newCPUs := int64(resizeResult.NewCPUs)
			if err := withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxAllocatedCPUs, exedb.UpdateBoxAllocatedCPUsParams{
				AllocatedCpus: &newCPUs,
				ID:            box.ID,
			}); err != nil {
				slog.ErrorContext(ctx, "failed to update allocated_cpus after resize", "box", boxName, "err", err)
			}
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
	cc.Writeln("  ssh %s sudo shutdown -r now", ss.server.env.BoxDest(boxName))
	if diskGrowResult != nil {
		// Newer exeuntu images automatically run resize2fs on boot.
		// Only show the manual resize2fs hint for non-exeuntu images or
		// VMs created before the automatic resize2fs was deployed.
		needsManualResize2fs := !strings.Contains(box.Image, "exeuntu")
		if !needsManualResize2fs && box.CreatedAt != nil {
			needsManualResize2fs = box.CreatedAt.Before(time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC))
		}
		if needsManualResize2fs {
			cc.Writeln("After restart, run resize2fs inside the VM:")
			cc.Writeln("  ssh %s sudo resize2fs /dev/vda", ss.server.env.BoxDest(boxName))
		}
	}

	return nil
}

func (ss *SSHServer) handleBackfillAllocatedCPUs(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: backfill-allocated-cpus <count>")
	}

	limit, err := strconv.ParseInt(cc.Args[0], 10, 64)
	if err != nil || limit <= 0 {
		return cc.Errorf("count must be a positive integer")
	}

	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxesWithNullAllocatedCPUs, limit)
	if err != nil {
		return cc.Errorf("failed to query boxes: %v", err)
	}

	if len(boxes) == 0 {
		cc.Writeln("No boxes with NULL allocated_cpus found.")
		return nil
	}

	cc.Writeln("Found %d boxes to backfill.", len(boxes))

	var updated, skipped, failed int
	for _, box := range boxes {
		if box.ContainerID == nil || *box.ContainerID == "" {
			skipped++
			continue
		}

		exeletClient := ss.server.getExeletClient(box.Ctrhost)
		if exeletClient == nil {
			cc.Writeln("  %s: no exelet client for %s, skipping", box.Name, box.Ctrhost)
			skipped++
			continue
		}

		instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
			ID: *box.ContainerID,
		})
		if err != nil {
			cc.Writeln("  %s: GetInstance failed: %v", box.Name, err)
			failed++
			continue
		}

		if instanceResp.Instance == nil || instanceResp.Instance.VMConfig == nil {
			cc.Writeln("  %s: no VMConfig in response, skipping", box.Name)
			skipped++
			continue
		}

		cpus := int64(instanceResp.Instance.VMConfig.CPUs)
		if cpus == 0 {
			cc.Writeln("  %s: CPUs=0 in VMConfig, skipping", box.Name)
			skipped++
			continue
		}

		if err := withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxAllocatedCPUs, exedb.UpdateBoxAllocatedCPUsParams{
			AllocatedCpus: &cpus,
			ID:            box.ID,
		}); err != nil {
			cc.Writeln("  %s: DB update failed: %v", box.Name, err)
			failed++
			continue
		}

		cc.Writeln("  %s: set allocated_cpus=%d", box.Name, cpus)
		updated++
	}

	cc.Writeln("Done. updated=%d skipped=%d failed=%d", updated, skipped, failed)
	return nil
}
