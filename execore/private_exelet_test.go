package execore

import (
	"context"
	"fmt"
	"testing"

	"exe.dev/exedb"
	"exe.dev/region"
)

func TestPrivateExelet(t *testing.T) {
	t.Parallel()
	pdxRegion, _ := region.ByCode("pdx")

	makeExelet := func(addr string) *exeletClient {
		ec := &exeletClient{addr: addr, region: pdxRegion}
		ec.up.Store(true)
		return ec
	}

	createTeam := func(t *testing.T, server *Server, teamID, displayName, userID string) {
		t.Helper()
		ctx := context.Background()
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			if err := q.InsertTeam(ctx, exedb.InsertTeamParams{
				TeamID:      teamID,
				DisplayName: displayName,
			}); err != nil {
				return err
			}
			return q.InsertTeamMember(ctx, exedb.InsertTeamMemberParams{
				TeamID: teamID,
				UserID: userID,
				Role:   "billing_owner",
			})
		})
		if err != nil {
			t.Fatalf("failed to create team: %v", err)
		}
	}

	markPrivate := func(t *testing.T, server *Server, addr string) {
		t.Helper()
		ctx := context.Background()
		err := exedb.WithTx1(server.db, ctx, (*exedb.Queries).InsertPrivateExelet, addr)
		if err != nil {
			t.Fatalf("failed to mark exelet private: %v", err)
		}
	}

	assignTeamExelet := func(t *testing.T, server *Server, teamID, addr string) {
		t.Helper()
		ctx := context.Background()
		err := exedb.WithTx1(server.db, ctx, (*exedb.Queries).InsertTeamExelet, exedb.InsertTeamExeletParams{
			TeamID:     teamID,
			ExeletAddr: addr,
		})
		if err != nil {
			t.Fatalf("failed to assign team exelet: %v", err)
		}
	}

	t.Run("private exelet excluded from normal user", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")

		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		markPrivate(t, server, privExelet.addr)

		userID := createTestUser(t, server, "normal@example.com")

		// Normal user should only get the public exelet.
		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr == privExelet.addr {
				t.Fatalf("normal user was scheduled onto private exelet")
			}
		}
	})

	t.Run("private exelet blocks all users when no team assigned", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		// Only one exelet and it's private. User should get an error.
		privExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			privExelet.addr: privExelet,
		}

		markPrivate(t, server, privExelet.addr)

		userID := createTestUser(t, server, "blocked@example.com")

		_, _, err := server.selectExeletClient(ctx, userID)
		if err == nil {
			t.Fatal("expected error when only private exelets available and user has no team")
		}
	})

	t.Run("team member can use private exelet assigned to their team", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		privExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "team-user@example.com")
		createTeam(t, server, "team-alpha", "Team Alpha", userID)

		markPrivate(t, server, privExelet.addr)
		assignTeamExelet(t, server, "team-alpha", privExelet.addr)

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.addr != privExelet.addr {
			t.Errorf("expected private exelet %s, got %s", privExelet.addr, ec.addr)
		}
	})

	t.Run("team member on wrong team cannot use private exelet", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "other-team@example.com")
		createTeam(t, server, "team-beta", "Team Beta", userID)

		markPrivate(t, server, privExelet.addr)
		// Assign to team-alpha, but user is in team-beta.
		err := exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.InsertTeam(ctx, exedb.InsertTeamParams{TeamID: "team-alpha", DisplayName: "Team Alpha"})
		})
		if err != nil {
			t.Fatalf("failed to create team-alpha: %v", err)
		}
		assignTeamExelet(t, server, "team-alpha", privExelet.addr)

		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr == privExelet.addr {
				t.Fatalf("user on wrong team was scheduled onto private exelet")
			}
		}
	})

	t.Run("affinity skipped for private exelet", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "affinity@example.com")

		// Create a VM on the private exelet to establish affinity.
		err := exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
				Ctrhost:         privExelet.addr,
				Name:            "test-priv-box",
				Status:          "running",
				Image:           "test",
				CreatedByUserID: userID,
				Region:          "pdx",
			})
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert box: %v", err)
		}

		markPrivate(t, server, privExelet.addr)

		// Affinity should be skipped because the exelet is now private.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.addr == privExelet.addr {
			t.Errorf("affinity to private exelet should have been skipped")
		}
	})

	t.Run("preferred exelet skipped when private", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		markPrivate(t, server, privExelet.addr)

		// Set the private exelet as preferred.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, privExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		userID := createTestUser(t, server, "pref@example.com")

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.addr == privExelet.addr {
			t.Errorf("preferred private exelet should have been skipped for non-team user")
		}
	})

	t.Run("non-private exelet with team mapping allows all users", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		// A team_exelets mapping on a non-private exelet has no exclusion effect.
		exelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			exelet.addr: exelet,
		}

		// Create team and assign, but don't mark private.
		userID := createTestUser(t, server, "anyone@example.com")
		createTeam(t, server, "team-gamma", "Team Gamma", userID)
		assignTeamExelet(t, server, "team-gamma", exelet.addr)

		// Non-team user should still be able to use it.
		otherUserID := createTestUser(t, server, "other@example.com")
		ec, _, err := server.selectExeletClient(ctx, otherUserID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.addr != exelet.addr {
			t.Errorf("expected exelet %s, got %s", exelet.addr, ec.addr)
		}
	})

	t.Run("affinity to public exelet skipped when team has assigned exelets", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "affinity-team@example.com")
		createTeam(t, server, "team-affinity", "Team Affinity", userID)

		markPrivate(t, server, privExelet.addr)
		assignTeamExelet(t, server, "team-affinity", privExelet.addr)

		// Create existing VMs on the public exelet to establish affinity.
		for i := range 3 {
			err := exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
				_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
					Ctrhost:         pubExelet.addr,
					Name:            fmt.Sprintf("pub-box-%d", i),
					Status:          "running",
					Image:           "test",
					CreatedByUserID: userID,
					Region:          "pdx",
				})
				return err
			})
			if err != nil {
				t.Fatalf("failed to insert box: %v", err)
			}
		}

		// Despite affinity to public exelet, should land on team exelet.
		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr != privExelet.addr {
				t.Fatalf("team member landed on %s, want team-assigned %s", ec.addr, privExelet.addr)
			}
		}
	})

	t.Run("team member prefers team-assigned exelet over public", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "team-prefer@example.com")
		createTeam(t, server, "team-delta", "Team Delta", userID)

		markPrivate(t, server, privExelet.addr)
		assignTeamExelet(t, server, "team-delta", privExelet.addr)

		// Team member should always land on their private exelet,
		// not be randomly scattered across public + private.
		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr != privExelet.addr {
				t.Fatalf("team member landed on %s, want team-assigned %s", ec.addr, privExelet.addr)
			}
		}
	})

	t.Run("team member falls back to public when team exelet is down", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		privExelet.up.Store(false) // team exelet is down
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "team-fallback@example.com")
		createTeam(t, server, "team-epsilon", "Team Epsilon", userID)

		markPrivate(t, server, privExelet.addr)
		assignTeamExelet(t, server, "team-epsilon", privExelet.addr)

		// Team exelet is down, should fall back to public.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.addr != pubExelet.addr {
			t.Fatalf("expected fallback to public %s, got %s", pubExelet.addr, ec.addr)
		}
	})

	t.Run("preferred exelet skipped for team member with different team exelet", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pubExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		privExelet := makeExelet("tcp://exelet-pdx2-prod-01:9080")
		server.exeletClients = map[string]*exeletClient{
			pubExelet.addr:  pubExelet,
			privExelet.addr: privExelet,
		}

		userID := createTestUser(t, server, "team-nopref@example.com")
		createTeam(t, server, "team-zeta", "Team Zeta", userID)

		markPrivate(t, server, privExelet.addr)
		assignTeamExelet(t, server, "team-zeta", privExelet.addr)

		// Set the public exelet as globally preferred.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, pubExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		// Team member should land on their private exelet, not the preferred public one.
		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr != privExelet.addr {
				t.Fatalf("team member landed on %s, want team-assigned %s", ec.addr, privExelet.addr)
			}
		}
	})

	t.Run("team exelet in different region overrides user region", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		nycRegion, _ := region.ByCode("nyc")

		pdxExelet := makeExelet("tcp://exelet-pdx1-prod-01:9080")
		nycExelet := &exeletClient{addr: "tcp://exelet-nyc1-prod-01:9080", region: nycRegion}
		nycExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			nycExelet.addr: nycExelet,
		}

		// User is in PDX, team exelet is in NYC.
		userID := createTestUser(t, server, "cross-region@example.com")
		createTeam(t, server, "team-xregion", "Team Cross-Region", userID)

		markPrivate(t, server, nycExelet.addr)
		assignTeamExelet(t, server, "team-xregion", nycExelet.addr)

		// Should land on the NYC team exelet despite user being in PDX.
		for range 20 {
			ec, _, err := server.selectExeletClient(ctx, userID)
			if err != nil {
				t.Fatalf("selectExeletClient: %v", err)
			}
			if ec.addr != nycExelet.addr {
				t.Fatalf("team member landed on %s, want cross-region team exelet %s", ec.addr, nycExelet.addr)
			}
		}
	})

	t.Run("exeletAllowsUser unit tests", func(t *testing.T) {
		t.Parallel()

		private := map[string]bool{"tcp://priv:9080": true}
		teamAddrs := map[string]bool{"tcp://priv:9080": true}

		tests := []struct {
			name            string
			addr            string
			teamExeletAddrs map[string]bool
			want            bool
		}{
			{"public exelet, no team", "tcp://pub:9080", nil, true},
			{"public exelet, with team", "tcp://pub:9080", teamAddrs, true},
			{"private exelet, no team", "tcp://priv:9080", nil, false},
			{"private exelet, wrong team addrs", "tcp://priv:9080", map[string]bool{"tcp://other:9080": true}, false},
			{"private exelet, correct team", "tcp://priv:9080", teamAddrs, true},
		}

		for _, tt := range tests {
			if got := exeletAllowsUser(tt.addr, private, tt.teamExeletAddrs); got != tt.want {
				t.Errorf("%s: exeletAllowsUser(%q) = %v, want %v", tt.name, tt.addr, got, tt.want)
			}
		}
	})
}
