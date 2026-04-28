package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// reflectionIntegrationConfig configures a reflection integration.
//
// Fields lists which metadata entries the integration exposes. Supported
// values are listed in reflectionFieldsAll.
type reflectionIntegrationConfig struct {
	Fields []string `json:"fields"`
}

// Supported reflection fields.
const (
	reflectionFieldEmail        = "email"
	reflectionFieldIntegrations = "integrations"
	reflectionFieldTags         = "tags"
	reflectionFieldComment      = "comment"
)

var reflectionFieldsAll = []string{
	reflectionFieldEmail,
	reflectionFieldIntegrations,
	reflectionFieldTags,
	reflectionFieldComment,
}

func isValidReflectionField(f string) bool {
	for _, v := range reflectionFieldsAll {
		if v == f {
			return true
		}
	}
	return false
}

// parseReflectionFields parses a comma-separated list of field names,
// validating each. An empty input is rejected (user must opt-in explicitly;
// use "none" to disable everything).
func parseReflectionFields(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "none" {
		return nil, nil
	}
	seen := map[string]bool{}
	var fields []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if f == "all" {
			for _, v := range reflectionFieldsAll {
				if !seen[v] {
					seen[v] = true
					fields = append(fields, v)
				}
			}
			continue
		}
		if !isValidReflectionField(f) {
			return nil, fmt.Errorf("unknown reflection field %q (valid: %s, all, none)", f, strings.Join(reflectionFieldsAll, ","))
		}
		if !seen[f] {
			seen[f] = true
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)
	return fields, nil
}

func (ss *SSHServer) handleAddReflection(ctx context.Context, cc *exemenu.CommandContext, attachments string, teamID *string) error {
	name := cc.FlagSet.Lookup("name").Value.String()
	fieldsRaw := cc.FlagSet.Lookup("fields").Value.String()

	if name == "" {
		return cc.Errorf("--name is required")
	}
	if err := validateIntegrationName(name); err != nil {
		return cc.Errorf("invalid name: %v", err)
	}
	if err := ss.checkIntegrationNameAvailable(ctx, cc, name); err != nil {
		return err
	}

	fields, err := parseReflectionFields(fieldsRaw)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	cfg := reflectionIntegrationConfig{Fields: fields}
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
			Type:          "reflection",
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
		cc.Writeln("Added team integration %s (reflection, fields=%s)", name, strings.Join(fields, ","))
	} else {
		cc.Writeln("Added integration %s (reflection, fields=%s)", name, strings.Join(fields, ","))
	}
	ss.printIntegrationUsage(cc, "reflection", name, attachments, nil, teamID)
	return nil
}
