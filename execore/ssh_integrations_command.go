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

// denyTagScopedIntegrationsMutation returns a user-facing error if the calling
// SSH key is tag-scoped. Tag-scoped keys may list integrations but must not
// add, remove, attach, detach, or rename them — those are account-level
// operations that could affect VMs outside the tag scope.
func denyTagScopedIntegrationsMutation(ctx context.Context, cc *exemenu.CommandContext) error {
	if perms := getSSHKeyPerms(ctx); perms != nil && perms.Tag != "" {
		return cc.Errorf("tag-scoped SSH keys cannot modify integrations")
	}
	return nil
}

// integrationsCommand returns the command definition for the hidden integrations command.
func (ss *SSHServer) integrationsCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:           "integrations",
		Aliases:        []string{"int"},
		AllowTagScoped: true,
		Description:    "Manage integrations",
		Usage:          "integrations <subcommand> [args...]",
		Handler:        ss.handleIntegrationsHelp,
		Subcommands: []*exemenu.Command{
			{
				Name:           "list",
				AllowTagScoped: true,
				Description:    "List your integrations",
				Usage:          "integrations list",
				Handler:        ss.handleIntegrationsList,
				FlagSetFunc:    jsonOnlyFlags("integrations-list"),
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
				Usage:             "integrations add <type> --name=<name> [--team] [args...]",
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
				Name: "attach",
				Description: "Attach an integration to a VM, tag, or all VMs" +
					"\n\nA <spec> controls where the integration is mounted:\n" +
					"  vm:<vm-name>   attach to a specific VM (personal only)\n" +
					"  tag:<tag-name> attach to every VM with the given tag\n" +
					"  auto:all       attach to all current and future VMs (personal only)\n\n" +
					"You can attach the same integration multiple times with different specs.\n" +
					"Team integrations only support tag:<tag-name>.",
				Usage:             "integrations attach <name> <spec>",
				Handler:           ss.handleIntegrationsAttach,
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeIntegrationAttachArgs,
				Examples: []string{
					"int attach my-mcp vm:dev1",
					"int attach my-mcp tag:production",
					"int attach my-mcp auto:all",
					"int attach shared-mcp tag:production",
				},
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

// findIntegrationByName looks up an integration by name, searching personal
// integrations first, then team integrations (if the user is in a team).
// Returns the integration and a user-facing error if not found.
func (ss *SSHServer) findIntegrationByName(ctx context.Context, cc *exemenu.CommandContext, name string) (exedb.Integration, error) {
	// Try personal first.
	ig, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: cc.User.ID,
		Name:        name,
	})
	if err == nil {
		return ig, nil
	}

	// Try team.
	team, teamErr := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if teamErr == nil && team != nil {
		ig, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByTeamAndName, exedb.GetIntegrationByTeamAndNameParams{
			TeamID: &team.TeamID,
			Name:   name,
		})
		if err == nil {
			return ig, nil
		}
	}

	return exedb.Integration{}, cc.Errorf("integration %q not found", name)
}

// completeIntegrationAttachArgs provides tab completion for `integrations attach <name> <spec>`.
//
// Position 2 (<name>) completes integration names visible to the caller (personal +
// team). Position 3 (<spec>) enumerates concrete attachment targets — vm:<vm-name>
// for each of the user's boxes, tag:<tag-name> for each tag in use, and auto:all —
// then filters by the typed prefix. If the named integration is team-owned, only
// tag:<tag-name> candidates are offered (matches parseTeamAttachmentSpec).
func (ss *SSHServer) completeIntegrationAttachArgs(compCtx *exemenu.CompletionContext, cc *exemenu.CommandContext) []string {
	if ss == nil || ss.server == nil || cc == nil || cc.User == nil || cc.SSHSession == nil {
		return nil
	}
	ctx := cc.SSHSession.Context()
	prefix := compCtx.CurrentWord

	switch compCtx.Position {
	case 2:
		return ss.integrationNameCompletions(ctx, cc.User.ID, prefix)
	case 3:
		if len(compCtx.Words) < 3 {
			return nil
		}
		teamOnly := ss.integrationIsTeamOwned(ctx, cc.User.ID, compCtx.Words[2])
		return ss.integrationAttachSpecCompletions(ctx, cc.User.ID, prefix, teamOnly)
	default:
		return nil
	}
}

// integrationNameCompletions returns the user's personal and team integration
// names that start with prefix.
func (ss *SSHServer) integrationNameCompletions(ctx context.Context, userID, prefix string) []string {
	integrations, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListIntegrationsByUser, userID)
	if err != nil {
		return nil
	}
	var teamIntegrations []exedb.Integration
	if team, _ := ss.server.GetTeamForUser(ctx, userID); team != nil {
		teamIntegrations, _ = withRxRes1(ss.server, ctx, (*exedb.Queries).ListIntegrationsByTeam, &team.TeamID)
	}
	var out []string
	for _, ig := range integrations {
		if strings.HasPrefix(ig.Name, prefix) {
			out = append(out, ig.Name)
		}
	}
	for _, ig := range teamIntegrations {
		if strings.HasPrefix(ig.Name, prefix) {
			out = append(out, ig.Name)
		}
	}
	return out
}

// integrationIsTeamOwned reports whether the named integration is owned by the
// caller's team rather than by the caller personally. Personal names take
// precedence (matches findIntegrationByName).
func (ss *SSHServer) integrationIsTeamOwned(ctx context.Context, userID, name string) bool {
	if _, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: userID,
		Name:        name,
	}); err == nil {
		return false
	}
	team, err := ss.server.GetTeamForUser(ctx, userID)
	if err != nil || team == nil {
		return false
	}
	_, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByTeamAndName, exedb.GetIntegrationByTeamAndNameParams{
		TeamID: &team.TeamID,
		Name:   name,
	})
	return err == nil
}

// integrationAttachSpecCompletions returns concrete attachment-spec candidates
// (e.g. "vm:dev1", "tag:prod", "auto:all") matching prefix. If teamOnly is true,
// only tag:<name> candidates are returned.
func (ss *SSHServer) integrationAttachSpecCompletions(ctx context.Context, userID, prefix string, teamOnly bool) []string {
	boxes, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		return nil
	}

	var vmSpecs []string
	tagSet := make(map[string]bool)
	for _, b := range boxes {
		if !teamOnly {
			vmSpecs = append(vmSpecs, "vm:"+b.Name)
		}
		for _, t := range b.GetTags() {
			tagSet[t] = true
		}
	}
	tagSpecs := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tagSpecs = append(tagSpecs, "tag:"+t)
	}
	sort.Strings(vmSpecs)
	sort.Strings(tagSpecs)

	candidates := append(vmSpecs, tagSpecs...)
	if !teamOnly {
		candidates = append(candidates, "auto:all")
	}

	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// resolveTeamFlag checks the --team flag and returns the team ID if set.
// Returns an error if --team is set but the user is not in a team.
func (ss *SSHServer) resolveTeamFlag(ctx context.Context, cc *exemenu.CommandContext) (isTeam bool, teamID string, err error) {
	if cc.FlagSet == nil {
		return false, "", nil
	}
	teamFlag := cc.FlagSet.Lookup("team")
	if teamFlag == nil {
		return false, "", nil
	}
	if teamFlag.Value.String() != "true" {
		return false, "", nil
	}
	team, teamErr := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if teamErr != nil || team == nil {
		return false, "", cc.Errorf("--team requires being in a team")
	}
	return true, team.TeamID, nil
}

// checkIntegrationNameAvailable checks that no personal or team integration
// with the given name exists for this user. Returns a user-facing error if
// the name is already taken.
func (ss *SSHServer) checkIntegrationNameAvailable(ctx context.Context, cc *exemenu.CommandContext, name string) error {
	// Check personal.
	_, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: cc.User.ID,
		Name:        name,
	})
	if err == nil {
		return cc.Errorf("name %q is already in use", name)
	}

	// Check team.
	team, teamErr := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if teamErr == nil && team != nil {
		_, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetIntegrationByTeamAndName, exedb.GetIntegrationByTeamAndNameParams{
			TeamID: &team.TeamID,
			Name:   name,
		})
		if err == nil {
			return cc.Errorf("name %q is already in use", name)
		}
	}

	return nil
}

// parseTeamAttachmentSpec validates an attachment spec for a team integration.
// Team integrations only support tag:<name>.
func parseTeamAttachmentSpec(spec string) (string, error) {
	if !strings.HasPrefix(spec, "tag:") || len(spec) <= 4 {
		return "", fmt.Errorf("team integrations only support tag:<name> attachments, got %q", spec)
	}
	if err := validateTagName(spec[4:]); err != nil {
		return "", err
	}
	return spec, nil
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

	// Fetch team integrations if the user is in a team.
	var teamIntegrations []exedb.Integration
	team, _ := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if team != nil {
		teamIntegrations, _ = withRxRes1(ss.server, ctx, (*exedb.Queries).ListIntegrationsByTeam, &team.TeamID)
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
			items = append(items, map[string]any{
				"name":        ig.Name,
				"type":        ig.Type,
				"config":      json.RawMessage(ig.Config),
				"attachments": ig.GetAttachments(),
				"comment":     ig.Comment,
			})
		}
		for _, ig := range teamIntegrations {
			items = append(items, map[string]any{
				"name":        ig.Name,
				"type":        ig.Type,
				"config":      json.RawMessage(ig.Config),
				"attachments": ig.GetAttachments(),
				"comment":     ig.Comment,
				"team":        true,
			})
		}
		cc.WriteJSON(items)
		return nil
	}
	if !showNotify && len(integrations) == 0 && len(teamIntegrations) == 0 {
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
	for _, ig := range teamIntegrations {
		attachments := ig.GetAttachments()
		attachStr := "(none)"
		if len(attachments) > 0 {
			attachStr = strings.Join(attachments, " ")
		}
		cc.Writeln("%s  %s  %s  %s  (team)", ig.Name, ig.Type, summarizeConfig(ig.Type, ig.Config), attachStr)
	}
	return nil
}

func summarizeConfig(typ, configJSON string) string {
	switch typ {
	case "http-proxy":
		var cfg httpProxyConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			parts := []string{"target=" + redactURLPassword(cfg.Target)}
			if cfg.PeerVM != "" {
				parts = append(parts, "peer="+cfg.PeerVM)
			} else if cfg.Header != "" {
				parts = append(parts, "header="+cfg.Header)
			}
			return strings.Join(parts, " ")
		}
	case "github":
		var cfg githubIntegrationConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			return fmt.Sprintf("repos=%s", strings.Join(cfg.Repositories, ","))
		}
	case "reflection":
		var cfg reflectionIntegrationConfig
		if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
			if len(cfg.Fields) == 0 {
				return "fields=(none)"
			}
			return "fields=" + strings.Join(cfg.Fields, ",")
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
	// PeerVM is set when the integration was created with --peer.
	// It records which VM the auto-generated API key targets.
	PeerVM string `json:"peer_vm,omitempty"`
}

// printIntegrationUsage prints usage instructions after creating an integration.
// For github integrations, repositories should be the list of repos; for other types, nil.
func (ss *SSHServer) printIntegrationUsage(cc *exemenu.CommandContext, typ, name, attachments string, repositories []string, teamID *string) {
	var intHost string
	if teamID != nil {
		intHost = name + ".team." + ss.server.env.BoxHost
	} else {
		intHost = name + ".int." + ss.server.env.BoxHost
	}

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
		if teamID != nil {
			cc.Writeln("To use this integration, attach it to a tag:")
			cc.Writeln("  integrations attach %s tag:<tag-name>", name)
		} else {
			cc.Writeln("To use this integration, attach it to a VM first:")
			cc.Writeln("  integrations attach %s vm:<vm-name>", name)
		}
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
		cc.Writeln("  GH_HOST=%s gh repo view %s", intHost, repo)
	case "reflection":
		cc.Writeln("Usage from a VM:")
		if vmName != "" {
			cc.Writeln("  ssh %s curl %s://%s/", vmName, scheme, intHost)
		} else {
			cc.Writeln("  curl %s://%s/", scheme, intHost)
		}
	}
}

// stringSliceFlag is a flag.Value that collects repeated --flag=value into a slice.
type stringSliceFlag struct {
	values []string
}

func (f *stringSliceFlag) String() string { return strings.Join(f.values, ",") }
func (f *stringSliceFlag) Set(v string) error {
	f.values = append(f.values, v)
	return nil
}

// commentFromFlags returns the value of the --comment flag, or "" if not set.
func commentFromFlags(cc *exemenu.CommandContext) string {
	if cc == nil || cc.FlagSet == nil {
		return ""
	}
	f := cc.FlagSet.Lookup("comment")
	if f == nil {
		return ""
	}
	return f.Value.String()
}

var knownIntegrationTypes = map[string]bool{
	"http-proxy": true,
	"github":     true,
	"reflection": true,
}

func (ss *SSHServer) handleIntegrationsAdd(ctx context.Context, cc *exemenu.CommandContext) error {
	if err := denyTagScopedIntegrationsMutation(ctx, cc); err != nil {
		return err
	}
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations add <type> --name=<name> [args...]")
	}
	typeName := cc.Args[0]
	if !knownIntegrationTypes[typeName] {
		return cc.Errorf("unknown integration type %q (known types: %s)", typeName, strings.Join(knownIntegrationTypeNames(), ", "))
	}

	isTeam, teamID, err := ss.resolveTeamFlag(ctx, cc)
	if err != nil {
		return err
	}

	// Parse optional --attach flags (can be repeated).
	var attachments string
	if attachVal, ok := cc.FlagSet.Lookup("attach").Value.(*stringSliceFlag); ok && len(attachVal.values) > 0 {
		var specs []string
		for _, raw := range attachVal.values {
			var spec string
			if isTeam {
				spec, err = parseTeamAttachmentSpec(raw)
			} else {
				spec, err = parseAttachmentSpec(raw)
			}
			if err != nil {
				return cc.Errorf("%v", err)
			}
			if !isTeam {
				if err := ss.validateAttachmentSpec(ctx, cc, spec); err != nil {
					return err
				}
			}
			specs = append(specs, spec)
		}
		attachments = exedb.AttachmentsJSON(specs)
	}

	var teamIDPtr *string
	if isTeam {
		teamIDPtr = &teamID
	}

	switch typeName {
	case "http-proxy":
		return ss.handleAddHTTPProxy(ctx, cc, attachments, teamIDPtr)
	case "github":
		return ss.handleAddGitHub(ctx, cc, attachments, teamIDPtr)
	case "reflection":
		return ss.handleAddReflection(ctx, cc, attachments, teamIDPtr)
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
	fs.Bool("peer", false, "authenticate with a generated API key scoped to the target VM")
	fs.Var(&stringSliceFlag{}, "attach", "attach to a spec (vm:<name>, tag:<name>, or auto:all); can be repeated")
	fs.Bool("team", false, "create as a team integration")
	fs.String("comment", "", "optional free-form comment stored with the integration")
	fs.String("fields", "all", "comma-separated reflection fields to expose (email, integrations, tags, comment); 'all' exposes every field including ones added in the future; 'none' disables every field")
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

func (ss *SSHServer) handleAddHTTPProxy(ctx context.Context, cc *exemenu.CommandContext, attachments string, teamID *string) error {
	name := cc.FlagSet.Lookup("name").Value.String()
	target := cc.FlagSet.Lookup("target").Value.String()
	header := cc.FlagSet.Lookup("header").Value.String()
	bearer := cc.FlagSet.Lookup("bearer").Value.String()
	peerFlag := cc.FlagSet.Lookup("peer").Value.(flag.Getter).Get().(bool)

	if name == "" {
		return cc.Errorf("--name is required")
	}
	if err := validateIntegrationName(name); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}
	if err := ss.checkIntegrationNameAvailable(ctx, cc, name); err != nil {
		return err
	}

	// --peer is mutually exclusive with --header and --bearer.
	if peerFlag && (header != "" || bearer != "") {
		return cc.Errorf("--peer is mutually exclusive with --header and --bearer")
	}

	if peerFlag {
		if target == "" {
			return cc.Errorf("--target is required with --peer")
		}
		return ss.handleAddHTTPProxyWithPeer(ctx, cc, name, target, attachments)
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
			TeamID:        teamID,
			Comment:       commentFromFlags(cc),
		})
	})
	if err != nil {
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	if teamID != nil {
		cc.Writeln("Added team integration %s", name)
	} else {
		cc.Writeln("Added integration %s", name)
	}
	ss.printIntegrationUsage(cc, "http-proxy", name, attachments, nil, teamID)
	return nil
}

func (ss *SSHServer) handleAddGitHub(ctx context.Context, cc *exemenu.CommandContext, attachments string, teamID *string) error {
	name := cc.FlagSet.Lookup("name").Value.String()
	repositoryFlag := cc.FlagSet.Lookup("repository").Value.String()

	if name == "" {
		return cc.Errorf("--name is required")
	}
	if err := validateIntegrationName(name); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}
	if err := ss.checkIntegrationNameAvailable(ctx, cc, name); err != nil {
		return err
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
	ghInstall, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetGitHubInstallationByTarget, exedb.GetGitHubInstallationByTargetParams{
		UserID:             cc.User.ID,
		GitHubAccountLogin: repoOwner,
	})
	if errors.Is(err, sql.ErrNoRows) {
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
			TeamID:        teamID,
			Comment:       commentFromFlags(cc),
		})
	})
	if err != nil {
		return cc.Errorf("failed to add integration (name %q may already be in use)", name)
	}

	if teamID != nil {
		cc.Writeln("Added team integration %s", name)
	} else {
		cc.Writeln("Added integration %s", name)
	}
	ss.printIntegrationUsage(cc, "github", name, attachments, repositories, teamID)
	return nil
}

// isBuiltinIntegration reports whether name is a built-in synthetic integration
// that cannot be modified by the user.
func isBuiltinIntegration(name string) bool {
	return name == "notify"
}

func (ss *SSHServer) handleIntegrationsRemove(ctx context.Context, cc *exemenu.CommandContext) error {
	if err := denyTagScopedIntegrationsMutation(ctx, cc); err != nil {
		return err
	}
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations remove <name>")
	}
	if isBuiltinIntegration(cc.Args[0]) {
		return cc.Errorf("%s is a built-in integration and cannot be removed", cc.Args[0])
	}

	ig, err := ss.findIntegrationByName(ctx, cc, cc.Args[0])
	if err != nil {
		return err
	}

	if ig.IsTeam() {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			// Delete any SSH keys linked to this integration (e.g. peer integrations).
			if err := queries.DeleteSSHKeysByIntegrationID(ctx, &ig.IntegrationID); err != nil {
				return err
			}
			return queries.DeleteTeamIntegration(ctx, exedb.DeleteTeamIntegrationParams{
				IntegrationID: ig.IntegrationID,
				TeamID:        ig.TeamID,
			})
		})
	} else {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			// Delete any SSH keys linked to this integration (e.g. peer integrations).
			if err := queries.DeleteSSHKeysByIntegrationID(ctx, &ig.IntegrationID); err != nil {
				return err
			}
			return queries.DeleteIntegration(ctx, exedb.DeleteIntegrationParams{
				IntegrationID: ig.IntegrationID,
				OwnerUserID:   cc.User.ID,
			})
		})
	}
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
		if err := validateTagName(spec[4:]); err != nil {
			return cc.Errorf("%v", err)
		}
	case spec == "auto:all":
		// Nothing to validate.
	}
	return nil
}

func (ss *SSHServer) handleIntegrationsAttach(ctx context.Context, cc *exemenu.CommandContext) error {
	if err := denyTagScopedIntegrationsMutation(ctx, cc); err != nil {
		return err
	}
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations attach <name> <spec>\n  <spec> is vm:<vm-name>, tag:<tag-name>, or auto:all")
	}
	name := cc.Args[0]
	if isBuiltinIntegration(name) {
		return cc.Errorf("%s is a built-in integration and is already attached to all VMs", name)
	}
	rawSpec := cc.Args[1]

	ig, err := ss.findIntegrationByName(ctx, cc, name)
	if err != nil {
		return err
	}

	var spec string
	if ig.IsTeam() {
		spec, err = parseTeamAttachmentSpec(rawSpec)
	} else {
		spec, err = parseAttachmentSpec(rawSpec)
		if err == nil {
			err = ss.validateAttachmentSpec(ctx, cc, spec)
		}
	}
	if err != nil {
		return cc.Errorf("%v", err)
	}

	attachments := ig.GetAttachments()
	for _, a := range attachments {
		if a == spec {
			return cc.Errorf("%s is already attached via %s", name, spec)
		}
	}
	attachments = append(attachments, spec)

	if ig.IsTeam() {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateTeamIntegrationAttachments(ctx, exedb.UpdateTeamIntegrationAttachmentsParams{
				Attachments:   exedb.AttachmentsJSON(attachments),
				IntegrationID: ig.IntegrationID,
				TeamID:        ig.TeamID,
			})
		})
	} else {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateIntegrationAttachments(ctx, exedb.UpdateIntegrationAttachmentsParams{
				Attachments:   exedb.AttachmentsJSON(attachments),
				IntegrationID: ig.IntegrationID,
				OwnerUserID:   cc.User.ID,
			})
		})
	}
	if err != nil {
		return cc.Errorf("failed to attach %s via %s", name, spec)
	}

	cc.Writeln("Attached %s to %s", name, spec)
	return nil
}

func (ss *SSHServer) handleIntegrationsDetach(ctx context.Context, cc *exemenu.CommandContext) error {
	if err := denyTagScopedIntegrationsMutation(ctx, cc); err != nil {
		return err
	}
	if len(cc.Args) != 2 {
		return cc.Errorf("usage: integrations detach <name> <spec>\n  <spec> is vm:<vm-name>, tag:<tag-name>, or auto:all")
	}
	name := cc.Args[0]
	if isBuiltinIntegration(name) {
		return cc.Errorf("%s is a built-in integration and cannot be detached", name)
	}
	rawSpec := cc.Args[1]

	ig, err := ss.findIntegrationByName(ctx, cc, name)
	if err != nil {
		return err
	}

	var spec string
	if ig.IsTeam() {
		spec, err = parseTeamAttachmentSpec(rawSpec)
	} else {
		spec, err = parseAttachmentSpec(rawSpec)
	}
	if err != nil {
		return cc.Errorf("%v", err)
	}

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

	if ig.IsTeam() {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateTeamIntegrationAttachments(ctx, exedb.UpdateTeamIntegrationAttachmentsParams{
				Attachments:   exedb.AttachmentsJSON(newAttachments),
				IntegrationID: ig.IntegrationID,
				TeamID:        ig.TeamID,
			})
		})
	} else {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateIntegrationAttachments(ctx, exedb.UpdateIntegrationAttachmentsParams{
				Attachments:   exedb.AttachmentsJSON(newAttachments),
				IntegrationID: ig.IntegrationID,
				OwnerUserID:   cc.User.ID,
			})
		})
	}
	if err != nil {
		return cc.Errorf("failed to detach %s from %s", name, spec)
	}

	cc.Writeln("Detached %s from %s", name, spec)
	return nil
}

func (ss *SSHServer) handleIntegrationsRename(ctx context.Context, cc *exemenu.CommandContext) error {
	if err := denyTagScopedIntegrationsMutation(ctx, cc); err != nil {
		return err
	}
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
	if err := ss.checkIntegrationNameAvailable(ctx, cc, newName); err != nil {
		return err
	}

	ig, err := ss.findIntegrationByName(ctx, cc, oldName)
	if err != nil {
		return err
	}

	if ig.IsTeam() {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateTeamIntegrationName(ctx, exedb.UpdateTeamIntegrationNameParams{
				Name:          newName,
				IntegrationID: ig.IntegrationID,
				TeamID:        ig.TeamID,
			})
		})
	} else {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpdateIntegrationName(ctx, exedb.UpdateIntegrationNameParams{
				Name:          newName,
				IntegrationID: ig.IntegrationID,
				OwnerUserID:   cc.User.ID,
			})
		})
	}
	if err != nil {
		return cc.Errorf("failed to rename (name %q may already be in use)", newName)
	}

	cc.Writeln("Renamed integration %s to %s", oldName, newName)
	return nil
}
