package execore

import (
	"context"
	"testing"

	"exe.dev/region"
	"exe.dev/sqlite"
)

func TestSelectExeletRequiresUserMatch(t *testing.T) {
	pdxRegion, _ := region.ByCode("pdx")
	tyoRegion, _ := region.ByCode("tyo")

	t.Run("pdx user gets pdx when tyo requires match", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)
		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			tyoExelet.addr: tyoExelet,
		}

		// User defaults to pdx region.
		userID := createTestUser(t, server, "pdx-user@example.com")

		ec, addr, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx exelet, got %s (addr=%s)", ec.region.Code, addr)
		}
	})

	t.Run("tyo user can get tyo", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			tyoExelet.addr: tyoExelet,
		}

		userID := createTestUser(t, server, "tyo-user@example.com")

		// Change user's region to tyo.
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "tyo", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		ec, addr, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "tyo" {
			t.Errorf("expected tyo exelet, got %s (addr=%s)", ec.region.Code, addr)
		}
	})

	t.Run("pdx user gets error when only tyo available", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			tyoExelet.addr: tyoExelet,
		}

		// User defaults to pdx region.
		userID := createTestUser(t, server, "stuck-user@example.com")

		_, _, err := server.selectExeletClient(ctx, userID)
		if err == nil {
			t.Fatal("expected error when only RequiresUserMatch exelets are available for non-matching user")
		}
	})

	t.Run("non-matching region does not require user match", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		// LAX has RequiresUserMatch=false, so any user should be able to use it.
		laxRegion, _ := region.ByCode("lax")
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
		}

		// User is in pdx but LAX doesn't require user match.
		userID := createTestUser(t, server, "lax-fallback@example.com")

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lax" {
			t.Errorf("expected lax exelet, got %s", ec.region.Code)
		}
	})

	t.Run("tyo user gets both pdx and tyo as candidates", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)
		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			tyoExelet.addr: tyoExelet,
		}

		userID := createTestUser(t, server, "tyo-both@example.com")

		// Change user's region to tyo.
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "tyo", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		// tyo user should get either pdx or tyo — both are eligible.
		if ec.region.Code != "pdx" && ec.region.Code != "tyo" {
			t.Errorf("expected pdx or tyo, got %s", ec.region.Code)
		}
	})
}
