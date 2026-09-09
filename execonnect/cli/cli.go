// Package cli implements the execonnect command-line interface.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"text/tabwriter"

	"github.com/boldsoftware/exe.dev/execonnect"

	urfave "github.com/urfave/cli/v2"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxEnrollmentToken     = 16 << 10
	stateDirOption         = "state-dir"
	socketOption           = "socket"
	externalConnectionsURL = "https://exe.dev/integrations#external-connections"
)

var (
	isTerminal            = term.IsTerminal
	readTerminalLineInput = readTerminalLine
)

// Run executes execonnect with args and the supplied standard streams.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	normalizedArgs, err := normalizeGlobalPathOptions(args)
	if err != nil {
		return err
	}
	var unknownCommand string
	var unknownParent string
	var unknownCandidates []string
	app := newApp(stdin, stdout, stderr)
	app.CommandNotFound = func(commandContext *urfave.Context, command string) {
		unknownCommand = command
		if commandContext.Command != nil {
			unknownParent = commandContext.Command.Name
			unknownCandidates = unknownCandidates[:0]
			for _, candidate := range commandContext.Command.Subcommands {
				unknownCandidates = append(unknownCandidates, candidate.Name)
			}
		}
	}
	app.ExitErrHandler = func(*urfave.Context, error) {}

	appArgs := make([]string, 1, len(normalizedArgs)+1)
	appArgs[0] = "execonnect"
	appArgs = append(appArgs, normalizedArgs...)
	if err := app.RunContext(ctx, appArgs); err != nil {
		return err
	}
	if unknownCommand == "" {
		return nil
	}
	return unknownCommandError(unknownParent, unknownCommand, unknownCandidates)
}

func newApp(stdin io.Reader, stdout, stderr io.Writer) *urfave.App {
	app := urfave.NewApp()
	app.Name = "execonnect"
	app.Version = execonnect.BuildRevision()
	app.Usage = "connects private services to exe.dev through an External Connection"
	app.UsageText = "execonnect [global options] COMMAND [command options]"
	app.Description = rootDescription()
	app.Reader = stdin
	app.Writer = stdout
	app.ErrWriter = stderr
	app.Flags = []urfave.Flag{stateDirFlag(), socketFlag()}
	app.Before = func(commandContext *urfave.Context) error {
		if err := requireFlagValue(commandContext, stateDirOption, "execonnect"); err != nil {
			return err
		}
		return requireFlagValue(commandContext, socketOption, "execonnect")
	}
	app.Action = func(commandContext *urfave.Context) error {
		if commandContext.NArg() == 0 {
			return urfave.ShowAppHelp(commandContext)
		}
		app.CommandNotFound(commandContext, commandContext.Args().First())
		return nil
	}
	app.OnUsageError = usageError("execonnect")
	app.Commands = []*urfave.Command{
		enrollCommand(),
		endpointCommand(),
		serveCommand(stderr),
		statusCommand(),
	}
	return app
}

func normalizeGlobalPathOptions(args []string) ([]string, error) {
	globalOptions := make([]string, 0, len(args))
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			remaining = append(remaining, args[index:]...)
			break
		}
		name, value, inline, ok := globalPathOption(arg)
		if !ok {
			remaining = append(remaining, arg)
			continue
		}
		if inline {
			if value == "" {
				return nil, withHelp(fmt.Errorf("option --%s requires a value", name), "execonnect")
			}
			globalOptions = append(globalOptions, arg)
			continue
		}
		if index+1 >= len(args) || args[index+1] == "--" {
			return nil, withHelp(fmt.Errorf("option --%s requires a value", name), "execonnect")
		}
		globalOptions = append(globalOptions, arg, args[index+1])
		index++
	}
	return append(globalOptions, remaining...), nil
}

func globalPathOption(arg string) (name, value string, inline, ok bool) {
	option, ok := strings.CutPrefix(arg, "--")
	if !ok {
		return "", "", false, false
	}
	name, value, inline = strings.Cut(option, "=")
	if name != stateDirOption && name != socketOption {
		return "", "", false, false
	}
	return name, value, inline, true
}

func unknownCommandError(parent, command string, candidates []string) error {
	kind := "command"
	helpCommand := "execonnect"
	if parent == "endpoint" {
		kind = "endpoint command"
		helpCommand = "execonnect endpoint"
	}
	if suggestion := commandSuggestion(command, candidates); suggestion != "" {
		return fmt.Errorf("unknown %s %q; did you mean %q? Run '%s --help'", kind, command, suggestion, helpCommand)
	}
	return fmt.Errorf("unknown %s %q; run '%s --help'", kind, command, helpCommand)
}

func commandSuggestion(command string, candidates []string) string {
	command = strings.ToLower(command)
	best := ""
	bestDistance := len(command) + 1
	tied := false
	for _, candidate := range candidates {
		distance := editDistance(command, candidate)
		switch {
		case distance < bestDistance:
			best = candidate
			bestDistance = distance
			tied = false
		case distance == bestDistance:
			tied = true
		}
	}
	maxDistance := 1
	if len(command) >= 6 {
		maxDistance = 2
	}
	if tied || bestDistance > maxDistance || bestDistance*3 > max(len(command), len(best)) {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex := 1; leftIndex <= len(left); leftIndex++ {
		current[0] = leftIndex
		for rightIndex := 1; rightIndex <= len(right); rightIndex++ {
			replacementCost := 1
			if left[leftIndex-1] == right[rightIndex-1] {
				replacementCost = 0
			}
			current[rightIndex] = min(
				current[rightIndex-1]+1,
				previous[rightIndex]+1,
				previous[rightIndex-1]+replacementCost,
			)
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}

func enrollCommand() *urfave.Command {
	return &urfave.Command{
		Name:      "enroll",
		Usage:     "Exchange a one-time token for durable connector state",
		UsageText: "execonnect [global options] enroll [command options] [token]",
		Description: `Connect this machine to an External Connection in exe.dev.

1. Run ` + "`execonnect serve`" + ` in another terminal on Linux or macOS, or
   start the installed execonnect systemd service on Linux.
2. Open ` + externalConnectionsURL + `, create an External Connection, and
   copy its one-time token.
3. Run ` + "`execonnect enroll TOKEN`" + `, or run ` + "`execonnect enroll`" + ` and paste the token
   when prompted.
4. Add a service with ` + "`execonnect endpoint add NAME URL`" + `.`,
		Flags: []urfave.Flag{
			&urfave.StringFlag{
				Name:  "exed-url",
				Usage: "exed HTTPS URL",
				Value: execonnect.DefaultExedURL,
			},
			&urfave.BoolFlag{
				Name:  "force",
				Usage: "replace without confirmation (also valid for first enrollment)",
			},
		},
		Before: func(commandContext *urfave.Context) error {
			if err := requireFlagValue(commandContext, "exed-url", "execonnect enroll"); err != nil {
				return err
			}
			if commandContext.NArg() > 1 {
				return withHelp(errors.New("enroll accepts at most one positional token"), "execonnect enroll")
			}
			return nil
		},
		OnUsageError: usageError("execonnect enroll"),
		Action: func(commandContext *urfave.Context) error {
			status, err := execonnect.InspectLocalStatus(commandContext.Context, execonnect.StatusOptions{
				StateDir:   commandContext.String(stateDirOption),
				SocketPath: commandContext.String(socketOption),
			})
			if err != nil {
				return err
			}
			interactive := readerIsTerminal(commandContext.App.Reader)
			positionalToken := commandContext.Args().First()
			if interactive && commandContext.NArg() == 0 {
				if _, err := fmt.Fprintf(commandContext.App.ErrWriter, "Open %s, create an External Connection, and copy its one-time token.\n", externalConnectionsURL); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(commandContext.App.ErrWriter, "Press Ctrl+C to cancel."); err != nil {
					return err
				}
			}
			if status.Enrolled && !commandContext.Bool("force") {
				if !interactive {
					return fmt.Errorf("already enrolled as %s; non-interactive replacement requires --force", status.ExternalConnectionID)
				}
				replace, err := confirmReplacement(commandContext.Context, commandContext.App.Reader, commandContext.App.ErrWriter, status.ExternalConnectionID)
				if err != nil {
					return err
				}
				if !replace {
					_, err := fmt.Fprintln(commandContext.App.Writer, "Enrollment unchanged.")
					return err
				}
			}
			state, err := execonnect.Enroll(commandContext.Context, execonnect.EnrollOptions{
				StateDir:      commandContext.String(stateDirOption),
				SocketPath:    commandContext.String(socketOption),
				ExedURL:       commandContext.String("exed-url"),
				ExpectedOldID: status.ExternalConnectionID,
				ReadToken: func() (string, error) {
					if commandContext.NArg() == 1 {
						return positionalToken, nil
					}
					return readEnrollmentToken(commandContext.Context, commandContext.App.Reader, commandContext.App.ErrWriter)
				},
			})
			if err != nil {
				return conciseEnrollmentError(err)
			}
			if _, err := fmt.Fprintf(commandContext.App.Writer, "Enrolled external connection %s.\n", state.ExternalConnectionID); err != nil {
				return err
			}
			if status.EndpointCount == 0 {
				_, err = fmt.Fprintln(commandContext.App.Writer, "Next: execonnect endpoint add NAME URL")
			}
			return err
		},
	}
}

func endpointCommand() *urfave.Command {
	return &urfave.Command{
		Name:      "endpoint",
		Usage:     "Add, update, remove, or list local endpoints",
		UsageText: endpointUsageText,
		Description: `Manage endpoints through the running serve process.

NAME is an immutable 1-63 character lowercase DNS label. URLs use http, https,
or tcp; tcp requires an explicit port. Start serve before adding, updating, or
removing endpoints. To publish your first service, run:

  execonnect endpoint add NAME URL

See a subcommand's help for examples.`,
		Action: func(commandContext *urfave.Context) error {
			if commandContext.NArg() == 0 {
				return urfave.ShowSubcommandHelp(commandContext)
			}
			commandContext.App.CommandNotFound(commandContext, commandContext.Args().First())
			return nil
		},
		Subcommands: []*urfave.Command{
			endpointSetCommand(false),
			endpointSetCommand(true),
			endpointRemoveCommand(),
			endpointListCommand(),
		},
	}
}

func endpointSetCommand(update bool) *urfave.Command {
	name := "add"
	usage := "Add a new endpoint"
	description := `NAME is an immutable 1-63 character lowercase DNS label. URL must use http,
https, or tcp with no user info, non-root path, query, or fragment. http and
https default to ports 80 and 443; tcp requires an explicit port. HTTPS derives
its TLS server name from the URL host; --tls-server-name overrides it only for
HTTPS. At most 100 endpoints may be active.

Examples:
  execonnect endpoint add database tcp://db.internal:5432
  execonnect endpoint add web http://web.internal:8080
  execonnect endpoint add --tls-server-name api.internal api https://10.0.0.15:8443`
	if update {
		name = "update"
		usage = "Retarget an existing endpoint without changing its durable name"
		description = `NAME follows the lowercase DNS-label rule and must already exist. URL uses http,
https, or tcp; tcp requires a port, and --tls-server-name is HTTPS-only. There
is no rename operation.

Examples:
  execonnect endpoint update database tcp://db-new.internal:5432
  execonnect endpoint update --tls-server-name api.internal api https://10.0.0.16:8443`
	}
	commandName := "execonnect endpoint " + name
	command := &urfave.Command{
		Name:        name,
		Usage:       usage,
		UsageText:   "execonnect [global options] endpoint " + name + " [command options] NAME URL",
		Description: description,
		Flags: []urfave.Flag{
			&urfave.StringFlag{
				Name:  "tls-server-name",
				Usage: "override the HTTPS TLS server name",
			},
		},
		OnUsageError: usageError(commandName),
	}
	command.Before = func(commandContext *urfave.Context) error {
		if err := requireFlagValue(commandContext, "tls-server-name", commandName); err != nil {
			return err
		}
		if commandContext.NArg() != 2 {
			return withHelp(fmt.Errorf("endpoint %s requires NAME and URL", name), commandName)
		}
		if _, err := execonnect.NormalizeEndpoint(commandContext.Args().Get(0), commandContext.Args().Get(1), commandContext.String("tls-server-name")); err != nil {
			return withHelp(err, commandName)
		}
		return nil
	}
	command.Action = func(commandContext *urfave.Context) error {
		options := execonnect.EndpointOptions{
			StateDir:      commandContext.String(stateDirOption),
			SocketPath:    commandContext.String(socketOption),
			Name:          commandContext.Args().Get(0),
			URL:           commandContext.Args().Get(1),
			TLSServerName: commandContext.String("tls-server-name"),
		}
		var changed bool
		var err error
		if update {
			_, changed, err = execonnect.UpdateEndpoint(commandContext.Context, options)
		} else {
			_, changed, err = execonnect.AddEndpoint(commandContext.Context, options)
		}
		if err != nil {
			return err
		}
		if !changed {
			_, err = fmt.Fprintf(commandContext.App.Writer, "Endpoint %s is unchanged.\n", options.Name)
			return err
		}
		verb := "Added"
		if update {
			verb = "Updated"
		}
		_, err = fmt.Fprintf(commandContext.App.Writer, "%s endpoint %s.\n", verb, options.Name)
		return err
	}
	return command
}

func endpointRemoveCommand() *urfave.Command {
	const commandName = "execonnect endpoint remove"
	return &urfave.Command{
		Name:      "remove",
		Usage:     "Remove an existing endpoint",
		UsageText: "execonnect [global options] endpoint remove NAME",
		Description: `Removal durably withdraws the endpoint, terminates established and pending flows
using it, and publishes the complete replacement endpoint set.

Example:
  execonnect endpoint remove database`,
		Before:       requireArgs(commandName, 1, "endpoint remove requires NAME"),
		OnUsageError: usageError(commandName),
		Action: func(commandContext *urfave.Context) error {
			name := commandContext.Args().First()
			if _, err := execonnect.RemoveEndpoint(commandContext.Context, execonnect.EndpointNameOptions{
				StateDir:   commandContext.String(stateDirOption),
				SocketPath: commandContext.String(socketOption),
				Name:       name,
			}); err != nil {
				return err
			}
			_, err := fmt.Fprintf(commandContext.App.Writer, "Removed endpoint %s.\n", name)
			return err
		},
	}
}

func endpointListCommand() *urfave.Command {
	const commandName = "execonnect endpoint list"
	return &urfave.Command{
		Name:      "list",
		Usage:     "List endpoints in deterministic name order",
		UsageText: "execonnect [global options] endpoint list",
		Description: `The table shows each canonical URL and effective HTTPS TLS server name.

Example:
  execonnect endpoint list`,
		Before:       requireArgs(commandName, 0, "endpoint list does not accept positional arguments"),
		OnUsageError: usageError(commandName),
		Action: func(commandContext *urfave.Context) error {
			snapshot, err := execonnect.ListEndpoints(commandContext.Context, execonnect.EndpointListOptions{
				StateDir:   commandContext.String(stateDirOption),
				SocketPath: commandContext.String(socketOption),
			})
			if err != nil {
				return err
			}
			return writeEndpointList(commandContext.App.Writer, snapshot.Endpoints)
		},
	}
}

func serveCommand(stderr io.Writer) *urfave.Command {
	const commandName = "execonnect serve"
	return &urfave.Command{
		Name:      "serve",
		Usage:     "Run the connector in the foreground",
		UsageText: "execonnect [global options] serve [command options]",
		Description: `serve stays in the foreground on Linux and macOS and is also the process run by
the installed Linux systemd unit. It owns all state, endpoint mutations, remote
connector sessions, and the local control socket. It remains available while
unenrolled and after terminal remote authentication rejection. Only one serve
process may own a control socket. --verbose includes raw gRPC detail.

Example:
  execonnect serve`,
		Flags: []urfave.Flag{
			&urfave.BoolFlag{
				Name:  "verbose",
				Usage: "include raw gRPC error details",
			},
		},
		Before:       requireArgs(commandName, 0, "serve does not accept positional arguments"),
		OnUsageError: usageError(commandName),
		Action: func(commandContext *urfave.Context) error {
			verbose := commandContext.Bool("verbose")
			logger := log.New(stderr, "execonnect: ", log.LstdFlags|log.LUTC)
			return execonnect.Serve(commandContext.Context, execonnect.ServeOptions{
				StateDir:   commandContext.String(stateDirOption),
				SocketPath: commandContext.String(socketOption),
				OnEvent: func(event execonnect.ServeEvent) {
					writeServeEvent(logger, event, verbose)
				},
			})
		},
	}
}

func statusCommand() *urfave.Command {
	const commandName = "execonnect status"
	return &urfave.Command{
		Name:      "status",
		Usage:     "Show daemon, enrollment, control, and tunnel status",
		UsageText: "execonnect [global options] status",
		Description: `status queries the running local daemon and reports its enrollment identity,
endpoint count, remote control state, tunnel state, and last session error. It
never reads daemon state directly or contacts exed. First-run output points to
` + externalConnectionsURL + ` and the next enrollment or endpoint command.

Example:
  execonnect status`,
		Before:       requireArgs(commandName, 0, "status does not accept positional arguments"),
		OnUsageError: usageError(commandName),
		Action: func(commandContext *urfave.Context) error {
			localStatus, err := execonnect.InspectLocalStatus(commandContext.Context, execonnect.StatusOptions{
				StateDir:   commandContext.String(stateDirOption),
				SocketPath: commandContext.String(socketOption),
			})
			if err != nil {
				return err
			}
			return writeStatus(commandContext.App.Writer, localStatus)
		},
	}
}

func stateDirFlag() urfave.Flag {
	return &urfave.StringFlag{
		Name:  stateDirOption,
		Usage: "daemon state directory; without --socket, also locates the control socket (root Linux: /var/lib/execonnect; non-root Linux: $XDG_STATE_HOME or $HOME; macOS: $HOME/Library/Application Support)",
	}
}

func socketFlag() urfave.Flag {
	return &urfave.StringFlag{
		Name:  socketOption,
		Usage: "local daemon control socket (root Linux: /run/execonnect/control.sock; non-root Linux: $XDG_RUNTIME_DIR or the state directory; macOS: the state directory)",
	}
}

func requireFlagValue(commandContext *urfave.Context, name, command string) error {
	if commandContext.IsSet(name) && commandContext.String(name) == "" {
		return withHelp(fmt.Errorf("option --%s requires a value", name), command)
	}
	return nil
}

func requireArgs(command string, count int, message string) urfave.BeforeFunc {
	return func(commandContext *urfave.Context) error {
		if commandContext.NArg() != count {
			return withHelp(errors.New(message), command)
		}
		return nil
	}
}

func usageError(command string) urfave.OnUsageErrorFunc {
	return func(_ *urfave.Context, err error, _ bool) error {
		return withHelp(err, command)
	}
}

func withHelp(err error, command string) error {
	return fmt.Errorf("%v; run '%s --help'", err, command)
}

func writeEndpointList(output io.Writer, endpoints []execonnect.Endpoint) error {
	if len(endpoints) == 0 {
		_, err := io.WriteString(output, "No endpoints configured.\nNext: execonnect endpoint add NAME URL\n")
		return err
	}
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tURL\tTLS SERVER NAME"); err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		tlsName := endpoint.TLSServerName
		if tlsName == "" {
			tlsName = "-"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", endpoint.Key, endpoint.URL, tlsName); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	_, err := output.Write(buffer.Bytes())
	return err
}

func writeStatus(output io.Writer, localStatus execonnect.LocalStatus) error {
	var buffer strings.Builder
	fmt.Fprintf(&buffer, "Service: %s\n", localStatus.Service)
	fmt.Fprintf(&buffer, "Enrollment: %s\n", localStatus.Enrollment)
	if localStatus.Enrolled {
		fmt.Fprintf(&buffer, "External connection ID: %s\n", localStatus.ExternalConnectionID)
		fmt.Fprintf(&buffer, "Exed URL: %s\n", localStatus.ExedURL)
	} else {
		fmt.Fprintln(&buffer, "External connection ID: -")
		fmt.Fprintln(&buffer, "Exed URL: -")
	}
	fmt.Fprintf(&buffer, "Endpoint count: %d\n", localStatus.EndpointCount)
	fmt.Fprintf(&buffer, "Control: %s\n", localStatus.Control)
	fmt.Fprintf(&buffer, "Tunnel: %s\n", localStatus.Tunnel)
	if localStatus.Error == "" {
		fmt.Fprintln(&buffer, "Error: -")
	} else {
		fmt.Fprintf(&buffer, "Error: %s\n", localStatus.Error)
	}
	switch {
	case !localStatus.Enrolled:
		fmt.Fprintf(&buffer, "Next: open %s and run execonnect enroll.\n", externalConnectionsURL)
	case localStatus.EndpointCount == 0:
		fmt.Fprintln(&buffer, "Next: execonnect endpoint add NAME URL")
	}
	_, err := io.WriteString(output, buffer.String())
	return err
}

func writeServeEvent(logger *log.Logger, event execonnect.ServeEvent, verbose bool) {
	switch event.Kind {
	case execonnect.ServeStarted:
		if event.ExternalConnectionID == "" {
			logger.Printf("daemon started unenrolled with %d endpoints", event.EndpointCount)
			logger.Printf("open %s and create an External Connection", externalConnectionsURL)
			logger.Print("run `execonnect enroll` in another terminal, then `execonnect endpoint add NAME URL`")
		} else {
			logger.Printf("daemon started for external connection %s with %d endpoints", event.ExternalConnectionID, event.EndpointCount)
		}
	case execonnect.ServeEndpointRegistryUpdated:
		logger.Printf("endpoints updated: count=%d", event.EndpointCount)
	case execonnect.ServeTunnelConfigured:
		logger.Print("tunnel configured")
	case execonnect.ServeControlConnected:
		if event.Attempt == 0 {
			logger.Print("control connected")
		} else {
			logger.Printf("control recovered after %d attempts", event.Attempt)
		}
	case execonnect.ServeControlRetrying:
		logger.Printf("control disconnected: %s; retrying in %s (attempt %d)", controlErrorText(event.Err, verbose), event.Delay, event.Attempt)
	case execonnect.ServeControlDisconnected:
		if event.Terminal {
			logger.Printf("connector session stopped: %s; daemon remains available for enrollment", controlErrorText(event.Err, verbose))
		}
	case execonnect.ServeEndpointSnapshotSent:
		if verbose {
			logger.Print("published endpoint snapshot")
		}
	case execonnect.ServeOutboundConnected:
		logger.Printf("outbound connection established target=%s", event.Target)
	case execonnect.ServeFlowError:
		logger.Print(event.Err)
	case execonnect.ServeShuttingDown:
		logger.Print("shutting down")
	case execonnect.ServeStopped:
		logger.Print("stopped")
	}
}

func conciseEnrollmentError(err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated:
		return errors.New("enrollment token was rejected or already used (Unauthenticated)")
	case codes.PermissionDenied:
		return errors.New("enrollment was denied (PermissionDenied)")
	case codes.InvalidArgument:
		return errors.New("enrollment request was rejected (InvalidArgument)")
	case codes.FailedPrecondition:
		return errors.New("external connection cannot be enrolled (FailedPrecondition)")
	case codes.Unknown:
		return err
	default:
		return fmt.Errorf("enrollment failed (%s)", status.Code(err))
	}
}

func controlErrorText(err error, verbose bool) string {
	if err == nil {
		return "unknown error"
	}
	if verbose {
		return err.Error()
	}
	code := status.Code(err)
	switch code {
	case codes.Unauthenticated:
		return "authentication rejected (Unauthenticated)"
	case codes.PermissionDenied:
		return "connector access denied (PermissionDenied)"
	case codes.InvalidArgument:
		return "connector configuration rejected (InvalidArgument)"
	case codes.FailedPrecondition:
		return "connector state rejected (FailedPrecondition)"
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted, codes.Internal:
		return code.String()
	default:
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "connection closed"
		}
		return err.Error()
	}
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(interface{ Fd() uintptr })
	return ok && isTerminal(int(file.Fd()))
}

func confirmReplacement(ctx context.Context, stdin io.Reader, stderr io.Writer, externalConnectionID string) (bool, error) {
	if _, err := fmt.Fprintf(stderr, "Replace enrollment %s? [y/N]: ", externalConnectionID); err != nil {
		return false, err
	}
	file, ok := stdin.(interface{ Fd() uintptr })
	if !ok {
		return false, fmt.Errorf("interactive replacement requires terminal input")
	}
	line, err := readTerminalLineInput(ctx, int(file.Fd()), false)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_, _ = io.WriteString(stderr, "\n")
			return false, err
		}
		return false, fmt.Errorf("read replacement confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(string(line)))
	return answer == "y" || answer == "yes", nil
}

func readEnrollmentToken(ctx context.Context, stdin io.Reader, stderr io.Writer) (string, error) {
	if file, ok := stdin.(interface{ Fd() uintptr }); ok && isTerminal(int(file.Fd())) {
		if _, err := io.WriteString(stderr, "Enrollment token: "); err != nil {
			return "", err
		}
		secret, err := readTerminalLineInput(ctx, int(file.Fd()), true)
		_, newlineErr := io.WriteString(stderr, "\n")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return "", err
			}
			return "", fmt.Errorf("read enrollment token: %w", err)
		}
		if newlineErr != nil {
			return "", newlineErr
		}
		if len(secret) > maxEnrollmentToken {
			return "", fmt.Errorf("enrollment token is too long")
		}
		return validateEnrollmentToken(string(secret))
	}
	limited := &io.LimitedReader{R: stdin, N: maxEnrollmentToken + 1}
	line, err := bufio.NewReader(limited).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read enrollment token: %w", err)
	}
	if len(line) > maxEnrollmentToken {
		return "", fmt.Errorf("enrollment token is too long")
	}
	return validateEnrollmentToken(line)
}

func validateEnrollmentToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("enrollment token is required on stdin")
	}
	return token, nil
}

func booleanState(value bool, trueValue, falseValue string) string {
	if value {
		return trueValue
	}
	return falseValue
}

func rootDescription() string {
	return `Path defaults are resolved only when a command needs them. Root Linux uses
/var/lib/execonnect for state and /run/execonnect/control.sock for its socket.
Non-root Linux follows XDG state/runtime directories with HOME-based fallbacks;
macOS uses ~/Library/Application Support/execonnect. A no-flag non-root Linux
serve uses its user socket; client commands try that socket, then the accessible
system socket. An explicit --state-dir also locates the socket unless --socket
is set. Global path options may appear before or after commands.

Quick start:
  Start execonnect serve in a foreground terminal on Linux or macOS, or start
  the installed execonnect systemd service on Linux.
  Open ` + externalConnectionsURL + ` and create an External Connection. Then, in another terminal:
    execonnect enroll
    execonnect endpoint add NAME URL

Run 'execonnect COMMAND --help' or 'execonnect endpoint SUBCOMMAND --help' for details.`
}

const endpointUsageText = `execonnect [global options] endpoint add [command options] NAME URL
   execonnect [global options] endpoint update [command options] NAME URL
   execonnect [global options] endpoint remove NAME
   execonnect [global options] endpoint list`
