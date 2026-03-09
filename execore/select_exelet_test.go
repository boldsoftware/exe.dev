package execore

import (
	"context"
	"testing"

	"exe.dev/exedb"
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

	t.Run("tyo user only gets tyo not pdx", func(t *testing.T) {
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
		// tyo user must stay in tyo — their region requires matching.
		if ec.region.Code != "tyo" {
			t.Errorf("expected tyo, got %s", ec.region.Code)
		}
	})

	t.Run("tyo user gets error when only pdx available", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
		}

		userID := createTestUser(t, server, "tyo-stuck@example.com")

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "tyo", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		_, _, err = server.selectExeletClient(ctx, userID)
		if err == nil {
			t.Fatal("expected error when RequiresUserMatch user has no exelets in their region")
		}
	})

	t.Run("pdx user still gets both pdx and lax", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		laxRegion, _ := region.ByCode("lax")
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			laxExelet.addr: laxExelet,
		}

		userID := createTestUser(t, server, "pdx-both@example.com")

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		// pdx user can get either pdx or lax — neither requires matching.
		if ec.region.Code != "pdx" && ec.region.Code != "lax" {
			t.Errorf("expected pdx or lax, got %s", ec.region.Code)
		}
	})

	t.Run("affinity skips exelet in RequiresUserMatch region for non-matching user", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			tyoExelet.addr: tyoExelet,
			pdxExelet.addr: pdxExelet,
		}

		// Create a pdx user with a VM on the tyo exelet (simulating a pre-existing placement).
		userID := createTestUser(t, server, "pdx-affinity@example.com")
		err := exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
				Ctrhost:         tyoExelet.addr,
				Name:            "test-box-tyo",
				Status:          "running",
				Image:           "test",
				CreatedByUserID: userID,
				Region:          "tyo",
			})
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert box: %v", err)
		}

		// Affinity would prefer tyo (has 1 VM there), but tyo.RequiresUserMatch
		// should block a pdx user. Must fall through to pdx.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx (affinity to tyo should be blocked), got %s", ec.region.Code)
		}
	})

	t.Run("affinity skips exelet when user RequiresUserMatch and exelet is different region", func(t *testing.T) {
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

		// Create a tyo user with a VM on the pdx exelet (pre-existing placement).
		userID := createTestUser(t, server, "tyo-affinity@example.com")
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "tyo", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}
		err = exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
				Ctrhost:         pdxExelet.addr,
				Name:            "test-box-pdx",
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

		// Affinity would prefer pdx (has 1 VM there), but tyo user's
		// RequiresUserMatch should block placement outside tyo.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "tyo" {
			t.Errorf("expected tyo (affinity to pdx should be blocked for tyo user), got %s", ec.region.Code)
		}
	})

	t.Run("preferred exelet skipped when in RequiresUserMatch region for non-matching user", func(t *testing.T) {
		server := newTestServer(t)
		ctx := context.Background()

		tyoExelet := &exeletClient{addr: "tcp://exelet-tyo1-prod-01:9080", region: tyoRegion}
		tyoExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			tyoExelet.addr: tyoExelet,
			pdxExelet.addr: pdxExelet,
		}

		// Set the preferred exelet to tyo.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, tyoExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		// pdx user should not land on the tyo preferred exelet.
		userID := createTestUser(t, server, "pdx-preferred@example.com")
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx (preferred tyo should be blocked), got %s", ec.region.Code)
		}
	})

	t.Run("preferred exelet skipped when user RequiresUserMatch and preferred is different region", func(t *testing.T) {
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

		// Set the preferred exelet to pdx.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, pdxExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		// tyo user should not land on the pdx preferred exelet.
		userID := createTestUser(t, server, "tyo-preferred@example.com")
		err = server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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
		if ec.region.Code != "tyo" {
			t.Errorf("expected tyo (preferred pdx should be blocked for tyo user), got %s", ec.region.Code)
		}
	})
}
