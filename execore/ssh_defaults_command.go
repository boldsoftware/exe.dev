package execore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// knownDefaultsKeys maps key names to their expected type for validation
var knownDefaultsKeys = map[string]string{
	"new-vm-email":       "bool",
	"anycast-network":    "int",
	"github-integration": "bool",
	"new.setup-script":   "text",
}

// defaultsCommand returns the command definition for the hidden defaults command
func (ss *SSHServer) defaultsCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "defaults",
		Hidden:      true,
		Description: "Read and write user defaults",
		Usage:       "defaults <subcommand> [args...]",
		Handler:     ss.handleDefaultsHelp,
		Subcommands: []*exemenu.Command{
			{
				Name:              "write",
				Description:       "Write a default value",
				Usage:             "defaults write <domain> <key> <value>",
				Handler:           ss.handleDefaultsWrite,
				HasPositionalArgs: true,
			},
			{
				Name:              "read",
				Description:       "Read a default value",
				Usage:             "defaults read <domain> [key]",
				Handler:           ss.handleDefaultsRead,
				HasPositionalArgs: true,
			},
			{
				Name:              "delete",
				Description:       "Delete a default value",
				Usage:             "defaults delete <domain> <key>",
				Handler:           ss.handleDefaultsDelete,
				HasPositionalArgs: true,
			},
		},
	}
}

func (ss *SSHServer) handleDefaultsHelp(ctx context.Context, cc *exemenu.CommandContext) error {
	cmd := ss.commands.FindCommand([]string{"defaults"})
	if cmd != nil {
		cmd.Help(cc)
	}
	return nil
}

// maxSetupScript is the maximum size of a setup script (10 KiB).
const maxSetupScript = 10 << 10

func (ss *SSHServer) handleDefaultsWrite(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 || len(cc.Args) > 3 {
		return cc.Errorf("usage: defaults write <domain> <key> <value>")
	}

	domain := cc.Args[0]
	key := cc.Args[1]

	if domain != "dev.exe" {
		return cc.Errorf("unknown domain %q (expected dev.exe)", domain)
	}

	expectedType, ok := knownDefaultsKeys[key]
	if !ok {
		return cc.Errorf("unknown key %q", key)
	}

	// For text type, value can come from stdin (piped) or as an argument.
	var value string
	if expectedType == "text" {
		if len(cc.Args) == 3 {
			value = cc.Args[2]
		} else if cc.IsSSHExec() && cc.SSHSession != nil {
			// Read from stdin
			data, err := io.ReadAll(io.LimitReader(cc.SSHSession, maxSetupScript+1))
			if err != nil {
				return cc.Errorf("reading from stdin: %v", err)
			}
			if len(data) > maxSetupScript {
				return cc.Errorf("input exceeds 10 KiB limit")
			}
			value = strings.TrimSpace(string(data))
			if value == "" {
				return cc.Errorf("stdin was empty")
			}
		} else {
			return cc.Errorf("usage: defaults write <domain> <key> <value>\nor pipe via stdin: cat script.sh | ssh exe.dev defaults write dev.exe %s", key)
		}
	} else {
		if len(cc.Args) != 3 {
			return cc.Errorf("usage: defaults write <domain> <key> <value>")
		}
		value = cc.Args[2]
	}

	switch expectedType {
	case "bool":
		var boolVal bool
		switch value {
		case "true", "1", "yes", "on":
			boolVal = true
		case "false", "0", "no", "off":
			boolVal = false
		default:
			return cc.Errorf("invalid value %q for %s (expected true or false)", value, key)
		}

		intVal := int64(0)
		if boolVal {
			intVal = 1
		}

		switch key {
		case "new-vm-email":
			return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertUserDefaultNewVMEmail(ctx, exedb.UpsertUserDefaultNewVMEmailParams{
					UserID:     cc.User.ID,
					NewVMEmail: &intVal,
				})
			})
		case "github-integration":
			return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertUserDefaultGitHubIntegration(ctx, exedb.UpsertUserDefaultGitHubIntegrationParams{
					UserID:            cc.User.ID,
					GitHubIntegration: &intVal,
				})
			})
		}
	case "int":
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return cc.Errorf("invalid value %q for %s (expected integer)", value, key)
		}

		switch key {
		case "anycast-network":
			if intVal != 1 && intVal != 2 {
				return cc.Errorf("invalid value %d for %s (expected 1 or 2)", intVal, key)
			}
			return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertUserDefaultAnycastNetwork(ctx, exedb.UpsertUserDefaultAnycastNetworkParams{
					UserID:         cc.User.ID,
					AnycastNetwork: &intVal,
				})
			})
		}
	case "text":
		if len(value) > maxSetupScript {
			return cc.Errorf("value exceeds 10 KiB limit")
		}
		switch key {
		case "new.setup-script":
			return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertUserDefaultNewSetupScript(ctx, exedb.UpsertUserDefaultNewSetupScriptParams{
					UserID:         cc.User.ID,
					NewSetupScript: &value,
				})
			})
		}
	}

	return cc.Errorf("unsupported key %q", key)
}

func (ss *SSHServer) handleDefaultsRead(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 || len(cc.Args) > 2 {
		return cc.Errorf("usage: defaults read <domain> [key]")
	}

	domain := cc.Args[0]
	if domain != "dev.exe" {
		return cc.Errorf("unknown domain %q (expected dev.exe)", domain)
	}

	defaults, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserDefaults, cc.User.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// If no key specified, show all
	if len(cc.Args) == 1 {
		cc.Writeln("new-vm-email: %s", formatBoolPtr(defaults.NewVMEmail))
		cc.Writeln("anycast-network: %s", formatIntPtr(defaults.AnycastNetwork))
		cc.Writeln("github-integration: %s", formatBoolPtr(defaults.GitHubIntegration))
		cc.Writeln("new.setup-script: %s", formatTextPtr(defaults.NewSetupScript))
		return nil
	}

	// Show specific key
	key := cc.Args[1]
	switch key {
	case "new-vm-email":
		cc.Writeln("%s", formatBoolPtr(defaults.NewVMEmail))
	case "anycast-network":
		cc.Writeln("%s", formatIntPtr(defaults.AnycastNetwork))
	case "github-integration":
		cc.Writeln("%s", formatBoolPtr(defaults.GitHubIntegration))
	case "new.setup-script":
		cc.Writeln("%s", formatTextPtr(defaults.NewSetupScript))
	default:
		return cc.Errorf("unknown key %q", key)
	}

	return nil
}

func (ss *SSHServer) handleDefaultsDelete(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: defaults delete <domain> <key>")
	}

	domain := cc.Args[0]
	key := cc.Args[1]

	if domain != "dev.exe" {
		return cc.Errorf("unknown domain %q (expected dev.exe)", domain)
	}

	if _, ok := knownDefaultsKeys[key]; !ok {
		return cc.Errorf("unknown key %q", key)
	}

	switch key {
	case "new-vm-email":
		return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteUserDefaultNewVMEmail(ctx, cc.User.ID)
		})
	case "anycast-network":
		return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteUserDefaultAnycastNetwork(ctx, cc.User.ID)
		})
	case "github-integration":
		return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteUserDefaultGitHubIntegration(ctx, cc.User.ID)
		})
	case "new.setup-script":
		return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteUserDefaultNewSetupScript(ctx, cc.User.ID)
		})
	}

	return nil
}

// formatBoolPtr formats an *int64 as a boolean string for display
func formatBoolPtr(v *int64) string {
	if v == nil {
		return "(not set)"
	}
	if *v == 0 {
		return "false"
	}
	return "true"
}

// formatIntPtr formats an *int64 as an integer string for display
func formatIntPtr(v *int64) string {
	if v == nil {
		return "(not set)"
	}
	return strconv.FormatInt(*v, 10)
}

// formatTextPtr formats a *string for display, truncating long values.
func formatTextPtr(v *string) string {
	if v == nil {
		return "(not set)"
	}
	s := *v
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// getUserDefaultSetupScript returns the user's default setup script, or "" if not set.
func (ss *SSHServer) getUserDefaultSetupScript(ctx context.Context, userID string) string {
	defaults, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if err != nil {
		return ""
	}
	if defaults.NewSetupScript == nil {
		return ""
	}
	return *defaults.NewSetupScript
}

// getUserDefaultNewVMEmail returns whether the user wants new-vm-email enabled.
// Returns true (send email) if not set or set to true, false if explicitly disabled.
func (ss *SSHServer) getUserDefaultNewVMEmail(ctx context.Context, userID string) bool {
	defaults, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if err != nil {
		return true // default to sending email on error
	}
	if defaults.NewVMEmail == nil {
		return true // default to sending email if not set
	}
	return *defaults.NewVMEmail != 0
}
