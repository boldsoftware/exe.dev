package execore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

var tagNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// validateTagName returns an error if name does not match tagNameRe.
func validateTagName(name string) error {
	if !tagNameRe.MatchString(name) {
		return fmt.Errorf("invalid tag name %q: must match %s", name, tagNameRe.String())
	}
	return nil
}

func (ss *SSHServer) handleTagCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	deleteMode := cc.FlagSet.Lookup("d").Value.String() == "true"

	if len(cc.Args) != 2 {
		return cc.Errorf("usage: tag [-d] <vm> <tag-name>")
	}

	// Tag-scoped keys cannot modify tags; the tag association is the basis of
	// their access and letting them add/remove tags would be privilege escalation.
	if perms := getSSHKeyPerms(ctx); perms != nil && perms.Tag != "" {
		return cc.Errorf("SSH key scoped to tag %q cannot modify tags", perms.Tag)
	}

	vmName := ss.normalizeBoxName(cc.Args[0])
	tagName := cc.Args[1]

	CommandLogAddAttr(ctx, slog.String("vm_name", vmName))
	CommandLogAddAttr(ctx, slog.String("tag_name", tagName))

	if !tagNameRe.MatchString(tagName) {
		return cc.Errorf("invalid tag name %q: must match [a-z][a-z0-9_-]*", tagName)
	}

	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, vmName)
	if err != nil {
		return cc.Errorf("vm %q not found", vmName)
	}

	CommandLogAddAttr(ctx, slog.Int("vm_id", box.ID))

	tags := box.GetTags()

	if deleteMode {
		idx := slices.Index(tags, tagName)
		if idx < 0 {
			return cc.Errorf("tag %q not found on %s", tagName, vmName)
		}
		tags = slices.Delete(tags, idx, idx+1)
	} else {
		if slices.Contains(tags, tagName) {
			return cc.Errorf("tag %q already exists on %s", tagName, vmName)
		}
		tags = append(tags, tagName)
		slices.Sort(tags)
	}

	tagsJSON := exedb.TagsJSON(tags)
	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxTags, exedb.UpdateBoxTagsParams{
		Tags: tagsJSON,
		ID:   box.ID,
	})
	if err != nil {
		return cc.Errorf("failed to update tags: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"vm_name": vmName,
			"tags":    tags,
		})
		return nil
	}

	if deleteMode {
		cc.Writeln("Removed tag %q from %s", tagName, vmName)
	} else {
		cc.Writeln("Added tag %q to %s", tagName, vmName)
	}
	return nil
}

// parseTags parses a tags JSON string into a slice.
func parseTags(tagsJSON string) []string {
	if tagsJSON == "" || tagsJSON == "[]" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		return nil
	}
	return tags
}
