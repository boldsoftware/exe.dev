package execore

import (
	"context"
	"testing"

	"exe.dev/exemenu"
	"exe.dev/sqlite"
	"github.com/stretchr/testify/assert"
)

// TestCompleteIntegrationName covers tab completion for `integrations attach <name>`.
// Verifies that the completer:
//   - returns matching personal integration names at the <name> position
//   - filters by the typed prefix
//   - is scoped to the calling user (no cross-user leakage)
//   - returns nil at positions other than the first positional arg
func TestCompleteIntegrationName(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID, _, _ := createTestUserWithIntegrationPerms(t, s)
	otherUserID, _, _ := createTestUserWithIntegrationPerms(t, s)

	ctx := context.Background()
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		insert := func(id, owner, name string) error {
			_, err := tx.Exec(
				`INSERT INTO integrations (integration_id, owner_user_id, type, name, config) VALUES (?, ?, 'http-proxy', ?, '{}')`,
				id, owner, name,
			)
			return err
		}
		if err := insert("int_alpha", userID, "alpha"); err != nil {
			return err
		}
		if err := insert("int_apple", userID, "apple"); err != nil {
			return err
		}
		if err := insert("int_banana", userID, "banana"); err != nil {
			return err
		}
		// An integration owned by a different user — must not appear.
		return insert("int_other", otherUserID, "alphabetical")
	})
	if err != nil {
		t.Fatalf("seed integrations: %v", err)
	}

	cc := &exemenu.CommandContext{
		User:       &exemenu.UserInfo{ID: userID, Email: userID + "@test.example.com"},
		PublicKey:  "test-key",
		SSHSession: &mockShellSession{},
	}

	tests := []struct {
		name     string
		line     string
		cursor   int
		expected []string
	}{
		{
			name:     "no prefix lists all of the user's integrations",
			line:     "integrations attach ",
			cursor:   len("integrations attach "),
			expected: []string{"alpha", "apple", "banana"},
		},
		{
			name:     "prefix b matches only banana",
			line:     "integrations attach b",
			cursor:   len("integrations attach b"),
			expected: []string{"banana"},
		},
		{
			name:     "prefix a matches both alpha and apple, not other-user's alphabetical",
			line:     "integrations attach a",
			cursor:   len("integrations attach a"),
			expected: []string{"alpha", "apple"},
		},
		{
			name:     "alias int works the same as integrations",
			line:     "int attach b",
			cursor:   len("int attach b"),
			expected: []string{"banana"},
		},
		{
			name:     "no completions past the spec arg (position 4)",
			line:     "integrations attach alpha vm:dev1 ",
			cursor:   len("integrations attach alpha vm:dev1 "),
			expected: nil,
		},
	}

	sshServer := s.sshServer
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sshServer.commands.CompleteCommand(tt.line, tt.cursor, cc)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.ElementsMatch(t, tt.expected, result)
			}
		})
	}
}

// TestCompleteIntegrationAttachSpec covers the <spec> arg of `integrations attach`.
// Verifies that the completer enumerates concrete vm:/tag:/auto:all candidates
// and filters by prefix, including the team-only restriction (tag:* only).
func TestCompleteIntegrationAttachSpec(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	userID, _, _ := createTestUserWithIntegrationPerms(t, s)

	ctx := context.Background()
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		// Two boxes; tags overlap so we exercise dedup.
		if _, err := tx.Exec(
			`INSERT INTO boxes (name, status, image, created_by_user_id, ctrhost, region, tags) VALUES (?, 'running', 'default', ?, 'tcp://local:9080', 'pdx', ?)`,
			"dev1", userID, `["prod","shared"]`,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO boxes (name, status, image, created_by_user_id, ctrhost, region, tags) VALUES (?, 'running', 'default', ?, 'tcp://local:9080', 'pdx', ?)`,
			"dev2", userID, `["staging","shared"]`,
		); err != nil {
			return err
		}
		// Personal integration "mine" and team integration "shared".
		if _, err := tx.Exec(
			`INSERT INTO integrations (integration_id, owner_user_id, type, name, config) VALUES (?, ?, 'http-proxy', ?, '{}')`,
			"int_mine", userID, "mine",
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO teams (team_id, display_name) VALUES ('team1', 'Team One')`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ('team1', ?, 'admin')`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO integrations (integration_id, owner_user_id, type, name, config, team_id) VALUES (?, ?, 'http-proxy', ?, '{}', 'team1')`,
			"int_team", userID, "shared",
		)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	cc := &exemenu.CommandContext{
		User:       &exemenu.UserInfo{ID: userID, Email: userID + "@test.example.com"},
		PublicKey:  "test-key",
		SSHSession: &mockShellSession{},
	}

	tests := []struct {
		name     string
		line     string
		expected []string
	}{
		{
			name:     "personal: empty prefix lists all spec candidates",
			line:     "integrations attach mine ",
			expected: []string{"vm:dev1", "vm:dev2", "tag:prod", "tag:shared", "tag:staging", "auto:all"},
		},
		{
			name:     "personal: 'v' filters to vm: candidates",
			line:     "integrations attach mine v",
			expected: []string{"vm:dev1", "vm:dev2"},
		},
		{
			name:     "personal: 'vm:d' filters by vm name prefix",
			line:     "integrations attach mine vm:d",
			expected: []string{"vm:dev1", "vm:dev2"},
		},
		{
			name:     "personal: 'tag:s' filters to tags starting with s",
			line:     "integrations attach mine tag:s",
			expected: []string{"tag:shared", "tag:staging"},
		},
		{
			name:     "personal: 'a' completes to auto:all",
			line:     "integrations attach mine a",
			expected: []string{"auto:all"},
		},
		{
			name:     "team integration: only tag:* candidates offered",
			line:     "integrations attach shared ",
			expected: []string{"tag:prod", "tag:shared", "tag:staging"},
		},
		{
			name:     "team integration: 'v' offers nothing (vm: is personal-only)",
			line:     "integrations attach shared v",
			expected: nil,
		},
		{
			name:     "alias int works for spec completion too",
			line:     "int attach mine vm:dev1",
			expected: []string{"vm:dev1"},
		},
	}

	sshServer := s.sshServer
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sshServer.commands.CompleteCommand(tt.line, len(tt.line), cc)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.ElementsMatch(t, tt.expected, result)
			}
		})
	}
}
