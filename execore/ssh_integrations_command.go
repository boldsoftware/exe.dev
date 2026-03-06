package execore

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// integrationsCommand returns the command definition for the hidden integrations command.
func (ss *SSHServer) integrationsCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "integrations",
		Hidden:      true,
		Description: "Manage integrations",
		Usage:       "integrations <subcommand> [args...]",
		Handler:     ss.handleIntegrationsHelp,
		Subcommands: []*exemenu.Command{
			{
				Name:        "list",
				Description: "List your integrations",
				Usage:       "integrations list",
				Handler:     ss.handleIntegrationsList,
			},
			{
				Name:              "add",
				Description:       "Add a new integration",
				Usage:             "integrations add <type> --name=<name> [args...]",
				Handler:           ss.handleIntegrationsAdd,
				HasPositionalArgs: true,
				FlagSetFunc:       addIntegrationFlags,
			},
			{
				Name:              "remove",
				Description:       "Remove an integration",
				Usage:             "integrations remove <name>",
				Handler:           ss.handleIntegrationsRemove,
				HasPositionalArgs: true,
			},
			{
				Name:              "attach",
				Description:       "Attach an integration to a VM",
				Usage:             "integrations attach <name> <vm-name>",
				Handler:           ss.handleIntegrationsAttach,
				HasPositionalArgs: true,
			},
			{
				Name:              "detach",
				Description:       "Detach an integration from a VM",
				Usage:             "integrations detach <name> <vm-name>",
				Handler:           ss.handleIntegrationsDetach,
				HasPositionalArgs: true,
			},
			{
				Name:              "rename",
				Description:       "Rename an integration",
				Usage:             "integrations rename <name> <new-name>",
				Handler:           ss.handleIntegrationsRename,
				HasPositionalArgs: true,
			},
		},
	}
}

// getIntegrationByName looks up an integration by name for the given user.
// Returns a user-facing error if not found.
func (ss *SSHServer) getIntegrationByName(ctx context.Context, cc *exemenu.CommandContext, userID, name string) (exedb.Integration, error) {
	ig, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: userID,
		Name:        name,
	})
	if err != nil {
		return exedb.Integration{}, cc.Errorf("integration %q not found", name)
	}
	return ig, nil
}

func (ss *SSHServer) handleIntegrationsHelp(ctx context.Context, cc *exemenu.CommandContext) error {
	cmd := ss.commands.FindCommand([]string{"integrations"})
	if cmd != nil {
		cmd.Help(cc)
	}
	return nil
}

func (ss *SSHServer) handleIntegrationsList(ctx context.Context, cc *exemenu.CommandContext) error {
	integrations, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListIntegrationsByUser, cc.User.ID)
	if err != nil {
		return err
	}
	if len(integrations) == 0 {
		cc.Writeln("No integrations configured.")
		return nil
	}
	for _, ig := range integrations {
		cc.Writeln("%s  %s  %s", ig.Name, ig.Type, summarizeConfig(ig.Type, ig.Config))
	}
	return nil
}

func summarizeConfig(typ, configJSON string) string {
	switch typ {
	case "http-proxy":
		var cfg httpProxyConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			return fmt.Sprintf("target=%s header=%s", cfg.Target, cfg.Header)
		}
	}
	return configJSON
}

type httpProxyConfig struct {
	Target string `json:"target"`
	Header string `json:"header"`
}

var knownIntegrationTypes = map[string]bool{
	"http-proxy": true,
}

func (ss *SSHServer) handleIntegrationsAdd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: integrations add <type> --name=<name> [args...]")
	}
	typeName := cc.Args[0]
	if !knownIntegrationTypes[typeName] {
		return cc.Errorf("unknown integration type %q (known types: %s)", typeName, strings.Join(knownIntegrationTypeNames(), ", "))
	}

	switch typeName {
	case "http-proxy":
		return ss.handleAddHTTPProxy(ctx, cc)
	default:
		return cc.Errorf("unknown integration type %q", typeName)
	}
}

func addIntegrationFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("integrations add", flag.ContinueOnError)
	fs.String("name", "", "integration name (required)")
	fs.String("target", "", "target URL (required for http-proxy)")
	fs.String("header", "", "header to inject (required for http-proxy)")
	fs.String("bearer", "", `bearer token (shorthand for --header="Authorization:Bearer TOKEN")`)
	return fs
}

func knownIntegrationTypeNames() []string {
	var names []string
	for k := range knownIntegrationTypes {
		names = append(names, k)
	}
	return names
}

func (ss *SSHServer) handleAddHTTPProxy(ctx context.Context, cc *exemenu.CommandContext) error {
	name := cc.FlagSet.Lookup("name").Value.String()
	target := cc.FlagSet.Lookup("target").Value.String()
	header := cc.FlagSet.Lookup("header").Value.String()
	bearer := cc.FlagSet.Lookup("bearer").Value.String()

	if name == "" {
		return cc.Errorf("--name is required")
	}
	if err := validateIntegrationName(name); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}
	if target == "" {
		return cc.Errorf("--target is required")
	}
	if err := validateTargetURL(target); err != nil {
		return cc.Errorf("%v", err)
	}
	if header != "" && bearer != "" {
		return cc.Errorf("--header and --bearer are mutually exclusive")
	}
	bearer = strings.TrimSpace(bearer)
	if bearer != "" {
		header = "Authorization:Bearer " + bearer
	}
	if header == "" {
		return cc.Errorf("--header (or --bearer) is required")
	}
	if err := validateHTTPHeader(header); err != nil {
		return cc.Errorf("invalid header: %v", err)
	}

	cfg := httpProxyConfig{Target: target, Header: header}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	id, err := generateID("int")
	if err != nil {
		return err
	}
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertIntegration(ctx, exedb.InsertIntegrationParams{
			IntegrationID: id,
			OwnerUserID:   cc.User.ID,
			Type:          "http-proxy",
			Config:        string(cfgJSON),
			Name:          name,
		})
	})
	if err != nil {
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	cc.Writeln("Added integration %s", name)
	return nil
}

func (ss *SSHServer) handleIntegrationsRemove(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations remove <name>")
	}
	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, cc.Args[0])
	if err != nil {
		return err
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteIntegration(ctx, exedb.DeleteIntegrationParams{
			IntegrationID: ig.IntegrationID,
			OwnerUserID:   cc.User.ID,
		})
	})
	if err != nil {
		return err
	}

	cc.Writeln("Removed integration %s", ig.Name)
	return nil
}

func (ss *SSHServer) handleIntegrationsAttach(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations attach <name> <vm-name>")
	}
	name := cc.Args[0]
	vmName := cc.Args[1]

	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, name)
	if err != nil {
		return err
	}

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertIntegrationAttachment(ctx, exedb.InsertIntegrationAttachmentParams{
			IntegrationID: ig.IntegrationID,
			BoxID:         int64(box.ID),
		})
	})
	if err != nil {
		return cc.Errorf("failed to attach: %s may already be attached to %s", name, vmName)
	}

	cc.Writeln("Attached %s to %s", name, vmName)
	return nil
}

func (ss *SSHServer) handleIntegrationsDetach(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations detach <name> <vm-name>")
	}
	name := cc.Args[0]
	vmName := cc.Args[1]

	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, name)
	if err != nil {
		return err
	}

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteIntegrationAttachment(ctx, exedb.DeleteIntegrationAttachmentParams{
			IntegrationID: ig.IntegrationID,
			BoxID:         int64(box.ID),
		})
	})
	if err != nil {
		return cc.Errorf("failed to detach")
	}

	cc.Writeln("Detached %s from %s", name, vmName)
	return nil
}

func (ss *SSHServer) handleIntegrationsRename(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations rename <name> <new-name>")
	}
	oldName := cc.Args[0]
	newName := cc.Args[1]

	if err := validateIntegrationName(newName); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}

	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, oldName)
	if err != nil {
		return err
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateIntegrationName(ctx, exedb.UpdateIntegrationNameParams{
			Name:          newName,
			IntegrationID: ig.IntegrationID,
			OwnerUserID:   cc.User.ID,
		})
	})
	if err != nil {
		return cc.Errorf("failed to rename (name %q may already be in use)", newName)
	}

	cc.Writeln("Renamed integration %s to %s", oldName, newName)
	return nil
}
