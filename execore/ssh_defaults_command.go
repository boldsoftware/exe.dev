package execore

import (
	"context"
	"database/sql"
	"errors"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// knownDefaultsKeys maps key names to their expected type for validation
var knownDefaultsKeys = map[string]string{
	"new-vm-email": "bool",
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

func (ss *SSHServer) handleDefaultsWrite(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 3 {
		return cc.Errorf("usage: defaults write <domain> <key> <value>")
	}

	domain := cc.Args[0]
	key := cc.Args[1]
	value := cc.Args[2]

	if domain != "dev.exe" {
		return cc.Errorf("unknown domain %q (expected dev.exe)", domain)
	}

	expectedType, ok := knownDefaultsKeys[key]
	if !ok {
		return cc.Errorf("unknown key %q", key)
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

		switch key {
		case "new-vm-email":
			intVal := int64(0)
			if boolVal {
				intVal = 1
			}
			return ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertUserDefaultNewVMEmail(ctx, exedb.UpsertUserDefaultNewVMEmailParams{
					UserID:     cc.User.ID,
					NewVMEmail: &intVal,
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
		return nil
	}

	// Show specific key
	key := cc.Args[1]
	switch key {
	case "new-vm-email":
		cc.Writeln("%s", formatBoolPtr(defaults.NewVMEmail))
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
