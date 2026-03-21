package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// integrationsCommand returns the command definition for the hidden integrations command.
func (ss *SSHServer) integrationsCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "integrations",
		Aliases:     []string{"int"},
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
				FlagSetFunc: jsonOnlyFlags("integrations-list"),
			},
			{
				Name:              "setup",
				Description:       "Set up a service integration",
				Usage:             "integrations setup <type> [-d]",
				Handler:           ss.handleIntegrationsSetup,
				HasPositionalArgs: true,
				FlagSetFunc:       setupGitHubFlags,
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
				Description:       "Attach an integration to a VM, tag, or all VMs",
				Usage:             "integrations attach <name> <spec>",
				Handler:           ss.handleIntegrationsAttach,
				HasPositionalArgs: true,
			},
			{
				Name:              "detach",
				Description:       "Detach an integration from a VM, tag, or all VMs",
				Usage:             "integrations detach <name> <spec>",
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

	// Only show the synthetic "notify" integration if the user has push tokens.
	hasPush, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).HasPushTokens, cc.User.ID)
	showNotify := hasPush != 0

	if cc.WantJSON() {
		var items []map[string]any
		if showNotify {
			items = append(items, map[string]any{
				"name":        "notify",
				"type":        "notify",
				"config":      json.RawMessage("{}"),
				"attachments": []string{"auto:all"},
				"builtin":     true,
			})
		}
		for _, ig := range integrations {
			item := map[string]any{
				"name":        ig.Name,
				"type":        ig.Type,
				"config":      json.RawMessage(ig.Config),
				"attachments": ig.GetAttachments(),
			}
			items = append(items, item)
		}
		cc.WriteJSON(items)
		return nil
	}
	if !showNotify && len(integrations) == 0 {
		cc.Writeln("No integrations configured.")
		return nil
	}
	if showNotify {
		cc.Writeln("%s  %s  %s  %s", "notify", "notify", "push notifications to device", "auto:all")
	}
	for _, ig := range integrations {
		attachments := ig.GetAttachments()
		attachStr := "(none)"
		if len(attachments) > 0 {
			attachStr = strings.Join(attachments, " ")
		}
		cc.Writeln("%s  %s  %s  %s", ig.Name, ig.Type, summarizeConfig(ig.Type, ig.Config), attachStr)
	}
	return nil
}

func summarizeConfig(typ, configJSON string) string {
	switch typ {
	case "http-proxy":
		var cfg httpProxyConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			parts := []string{"target=" + redactURLPassword(cfg.Target)}
			if cfg.Header != "" {
				parts = append(parts, "header="+cfg.Header)
			}
			return strings.Join(parts, " ")
		}
	case "github":
		var cfg githubIntegrationConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			return fmt.Sprintf("repos=%s", strings.Join(cfg.Repositories, ","))
		}
	}
	return configJSON
}

// redactURLPassword replaces the password in a URL's userinfo with "***".
func redactURLPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPass := u.User.Password(); hasPass {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

type githubIntegrationConfig struct {
	Repositories   []string `json:"repositories"`
	InstallationID int64    `json:"installation_id"`
}

type httpProxyConfig struct {
	Target string `json:"target"`
	Header string `json:"header"`
}

// printIntegrationUsage prints usage instructions after creating an integration.
// For github integrations, repositories should be the list of repos; for other types, nil.
func (ss *SSHServer) printIntegrationUsage(cc *exemenu.CommandContext, typ, name, attachments string, repositories []string) {
	intHost := name + ".int." + ss.server.env.BoxHost

	// Parse attachments to find a VM name for the example.
	var specList []string
	if attachments != "" && attachments != "[]" {
		json.Unmarshal([]byte(attachments), &specList)
	}

	var vmName string
	for _, spec := range specList {
		if strings.HasPrefix(spec, "vm:") {
			vmName = spec[3:]
			break
		}
	}

	hasAttachments := len(specList) > 0

	cc.Writeln("")
	if !hasAttachments {
		cc.Writeln("To use this integration, attach it to a VM first:")
		cc.Writeln("  integrations attach %s vm:<vm-name>", name)
		cc.Writeln("")
	}

	scheme := "http"
	if ss.server.servingHTTPS() {
		scheme = "https"
	}

	switch typ {
	case "http-proxy":
		cc.Writeln("Usage from a VM:")
		if vmName != "" {
			cc.Writeln("  ssh %s curl %s://%s/", vmName, scheme, intHost)
		} else {
			cc.Writeln("  curl %s://%s/", scheme, intHost)
		}
	case "github":
		repo := repositories[0]
		cc.Writeln("Usage from a VM:")
		if vmName != "" {
			cc.Writeln("  ssh %s git clone %s://%s/%s.git", vmName, scheme, intHost, repo)
		} else {
			cc.Writeln("  git clone %s://%s/%s.git", scheme, intHost, repo)
		}
	}
}

var knownIntegrationTypes = map[string]bool{
	"http-proxy": true,
	"github":     true,
}

func (ss *SSHServer) handleIntegrationsAdd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations add <type> --name=<name> [args...]")
	}
	typeName := cc.Args[0]
	if !knownIntegrationTypes[typeName] {
		return cc.Errorf("unknown integration type %q (known types: %s)", typeName, strings.Join(knownIntegrationTypeNames(), ", "))
	}

	// Parse optional --attach flag.
	var attachments string
	if attachFlag := cc.FlagSet.Lookup("attach").Value.String(); attachFlag != "" {
		spec, err := parseAttachmentSpec(attachFlag)
		if err != nil {
			return cc.Errorf("%v", err)
		}
		if err := ss.validateAttachmentSpec(ctx, cc, spec); err != nil {
			return err
		}
		attachments = exedb.AttachmentsJSON([]string{spec})
	}

	switch typeName {
	case "http-proxy":
		return ss.handleAddHTTPProxy(ctx, cc, attachments)
	case "github":
		return ss.handleAddGitHub(ctx, cc, attachments)
	default:
		return cc.Errorf("unknown integration type %q", typeName)
	}
}

func addIntegrationFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("integrations add", flag.ContinueOnError)
	fs.String("name", "", "integration name (required)")
	fs.String("target", "", "target URL (required for http-proxy)")
	fs.String("header", "", "header to inject (e.g. X-Auth:secret)")
	fs.String("bearer", "", `bearer token (shorthand for --header="Authorization:Bearer TOKEN")`)
	fs.String("repository", "", "GitHub repository in owner/repo format (required for github)")
	fs.String("attach", "", "attach to a spec (vm:<name>, tag:<name>, or auto:all)")
	return fs
}

func knownIntegrationTypeNames() []string {
	var names []string
	for k := range knownIntegrationTypes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (ss *SSHServer) handleAddHTTPProxy(ctx context.Context, cc *exemenu.CommandContext, attachments string) error {
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
	// A header is optional when the target URL contains basic auth credentials.
	targetURL, _ := url.Parse(target)
	hasBasicAuth := targetURL != nil && targetURL.User != nil
	if header == "" && !hasBasicAuth {
		return cc.Errorf("--header (or --bearer) is required")
	}
	if header != "" {
		if err := validateHTTPHeader(header); err != nil {
			return cc.Errorf("invalid header: %v", err)
		}
	}

	cfg := httpProxyConfig{Target: target, Header: header}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	if attachments == "" {
		attachments = "[]"
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
			Attachments:   attachments,
		})
	})
	if err != nil {
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	cc.Writeln("Added integration %s", name)
	ss.printIntegrationUsage(cc, "http-proxy", name, attachments, nil)
	return nil
}

func (ss *SSHServer) handleAddGitHub(ctx context.Context, cc *exemenu.CommandContext, attachments string) error {
	name := cc.FlagSet.Lookup("name").Value.String()
	repositoryFlag := cc.FlagSet.Lookup("repository").Value.String()

	if name == "" {
		return cc.Errorf("--name is required")
	}
	if err := validateIntegrationName(name); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}
	if repositoryFlag == "" {
		return cc.Errorf("--repository is required (e.g. owner/repo or owner/repo1,owner/repo2)")
	}

	// Parse comma-separated repositories.
	var repositories []string
	for _, r := range strings.Split(repositoryFlag, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		owner, repo, ok := strings.Cut(r, "/")
		if !ok || owner == "" || repo == "" {
			return cc.Errorf("--repository %q must be in owner/repo format", r)
		}
		repositories = append(repositories, r)
	}
	if len(repositories) == 0 {
		return cc.Errorf("--repository is required (e.g. owner/repo)")
	}

	// All repos must share the same owner (one installation = one account).
	owners := map[string]bool{}
	for _, r := range repositories {
		owner, _, _ := strings.Cut(r, "/")
		owners[owner] = true
	}
	if len(owners) > 1 {
		return cc.Errorf("all repositories must belong to the same owner")
	}
	var repoOwner string
	for o := range owners {
		repoOwner = o
	}

	// Look up the installation for this repo owner.
	// Check this before the server-level config so that users who haven't
	// connected GitHub get a more actionable error message.
	ghInstall, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetGitHubInstallationByTarget, exedb.GetGitHubInstallationByTargetParams{
		UserID:             cc.User.ID,
		GitHubAccountLogin: repoOwner,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// List what's connected to give a helpful error.
		accounts, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubInstallations, cc.User.ID)
		if len(accounts) == 0 {
			return cc.Errorf("no GitHub account connected; run: integrations setup github")
		}
		var connected []string
		for _, a := range accounts {
			connected = append(connected, a.GitHubAccountLogin)
		}
		return cc.Errorf("no GitHub App installed on %q; connected: %s. Run: integrations setup github", repoOwner, strings.Join(connected, ", "))
	}
	if err != nil {
		return err
	}

	if !ss.server.githubApp.InstallationTokensEnabled() {
		return cc.Errorf("GitHub installation tokens are not configured on this server")
	}

	cfg := githubIntegrationConfig{
		Repositories:   repositories,
		InstallationID: ghInstall.GitHubAppInstallationID,
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	if attachments == "" {
		attachments = "[]"
	}

	id, err := generateID("int")
	if err != nil {
		return err
	}
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertIntegration(ctx, exedb.InsertIntegrationParams{
			IntegrationID: id,
			OwnerUserID:   cc.User.ID,
			Type:          "github",
			Config:        string(cfgJSON),
			Name:          name,
			Attachments:   attachments,
		})
	})
	if err != nil {
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	cc.Writeln("Added integration %s", name)
	ss.printIntegrationUsage(cc, "github", name, attachments, repositories)
	return nil
}

// isBuiltinIntegration reports whether name is a built-in synthetic integration
// that cannot be modified by the user.
func isBuiltinIntegration(name string) bool {
	return name == "notify"
}

func (ss *SSHServer) handleIntegrationsRemove(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations remove <name>")
	}
	if isBuiltinIntegration(cc.Args[0]) {
		return cc.Errorf("%s is a built-in integration and cannot be removed", cc.Args[0])
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

// parseAttachmentSpec validates a user-supplied attachment spec.
// Must be vm:<name>, tag:<name>, or auto:all.
func parseAttachmentSpec(spec string) (string, error) {
	if spec == "auto:all" {
		return spec, nil
	}
	if strings.HasPrefix(spec, "vm:") && len(spec) > 3 {
		return spec, nil
	}
	if strings.HasPrefix(spec, "tag:") && len(spec) > 4 {
		return spec, nil
	}
	return "", fmt.Errorf("invalid attachment spec %q: must be vm:<name>, tag:<name>, or auto:all", spec)
}

// validateAttachmentSpec validates that the target of a parsed attachment spec exists.
func (ss *SSHServer) validateAttachmentSpec(ctx context.Context, cc *exemenu.CommandContext, spec string) error {
	switch {
	case strings.HasPrefix(spec, "vm:"):
		vmName := spec[3:]
		_, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
		if err != nil {
			return cc.Errorf("vm %q not found", vmName)
		}
	case strings.HasPrefix(spec, "tag:"):
		tagName := spec[4:]
		if !tagNameRe.MatchString(tagName) {
			return cc.Errorf("invalid tag name %q: must match %s", tagName, tagNameRe.String())
		}
	case spec == "auto:all":
		// Nothing to validate.
	}
	return nil
}

func (ss *SSHServer) handleIntegrationsAttach(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations attach <name> <spec>\n  <spec> is vm:<vm-name>, tag:<tag-name>, or auto:all")
	}
	name := cc.Args[0]
	if isBuiltinIntegration(name) {
		return cc.Errorf("%s is a built-in integration and is already attached to all VMs", name)
	}
	rawSpec := cc.Args[1]

	spec, err := parseAttachmentSpec(rawSpec)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, name)
	if err != nil {
		return err
	}

	if err := ss.validateAttachmentSpec(ctx, cc, spec); err != nil {
		return err
	}

	// Add to attachments list, checking for duplicates.
	attachments := ig.GetAttachments()
	for _, a := range attachments {
		if a == spec {
			return cc.Errorf("%s is already attached via %s", name, spec)
		}
	}
	attachments = append(attachments, spec)

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateIntegrationAttachments(ctx, exedb.UpdateIntegrationAttachmentsParams{
			Attachments:   exedb.AttachmentsJSON(attachments),
			IntegrationID: ig.IntegrationID,
			OwnerUserID:   cc.User.ID,
		})
	})
	if err != nil {
		return cc.Errorf("failed to attach %s via %s", name, spec)
	}

	cc.Writeln("Attached %s to %s", name, spec)
	return nil
}

func (ss *SSHServer) handleIntegrationsDetach(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations detach <name> <spec>\n  <spec> is vm:<vm-name>, tag:<tag-name>, or auto:all")
	}
	name := cc.Args[0]
	if isBuiltinIntegration(name) {
		return cc.Errorf("%s is a built-in integration and cannot be detached", name)
	}
	rawSpec := cc.Args[1]

	spec, err := parseAttachmentSpec(rawSpec)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	ig, err := ss.getIntegrationByName(ctx, cc, cc.User.ID, name)
	if err != nil {
		return err
	}

	// Remove the spec from the attachments list.
	attachments := ig.GetAttachments()
	found := false
	newAttachments := make([]string, 0, len(attachments))
	for _, a := range attachments {
		if a == spec {
			found = true
		} else {
			newAttachments = append(newAttachments, a)
		}
	}
	if !found {
		return cc.Errorf("%s is not attached via %s", name, spec)
	}

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateIntegrationAttachments(ctx, exedb.UpdateIntegrationAttachmentsParams{
			Attachments:   exedb.AttachmentsJSON(newAttachments),
			IntegrationID: ig.IntegrationID,
			OwnerUserID:   cc.User.ID,
		})
	})
	if err != nil {
		return cc.Errorf("failed to detach %s from %s", name, spec)
	}

	cc.Writeln("Detached %s from %s", name, spec)
	return nil
}

func (ss *SSHServer) handleIntegrationsRename(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations rename <name> <new-name>")
	}
	oldName := cc.Args[0]
	if isBuiltinIntegration(oldName) {
		return cc.Errorf("%s is a built-in integration and cannot be renamed", oldName)
	}
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
