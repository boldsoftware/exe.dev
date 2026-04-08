package execore

import (
	"context"
	"testing"

	"exe.dev/exedb"
	"exe.dev/region"
	"exe.dev/sqlite"
)

func TestSelectExeletRequiresUserMatch(t *testing.T) {
	t.Parallel()
	laxRegion, _ := region.ByCode("lax")
	pdxRegion, _ := region.ByCode("pdx")

	t.Run("lax user gets lax when pdx requires match", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
			pdxExelet.addr: pdxExelet,
		}

		// User defaults to lax region.
		userID := createTestUser(t, server, "lax-user@example.com")

		ec, addr, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lax" {
			t.Errorf("expected lax exelet, got %s (addr=%s)", ec.region.Code, addr)
		}
	})

	t.Run("pdx user can get pdx", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
		}

		userID := createTestUser(t, server, "pdx-user@example.com")

		// Change user's region to pdx.
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "pdx", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		ec, addr, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx exelet, got %s (addr=%s)", ec.region.Code, addr)
		}
	})

	t.Run("lax user gets error when only pdx available", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
		}

		// User defaults to lax region.
		userID := createTestUser(t, server, "stuck-user@example.com")

		_, _, err := server.selectExeletClient(ctx, userID)
		if err == nil {
			t.Fatal("expected error when only RequiresUserMatch exelets are available for non-matching user")
		}
	})

	t.Run("non-matching region does not require user match", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		// LAX has RequiresUserMatch=false, so any user should be able to use it.
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
		}

		// Default user is lax; LAX doesn't require user match.
		userID := createTestUser(t, server, "lax-fallback@example.com")

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lax" {
			t.Errorf("expected lax exelet, got %s", ec.region.Code)
		}
	})

	t.Run("pdx user only gets pdx not lax", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
			pdxExelet.addr: pdxExelet,
		}

		userID := createTestUser(t, server, "pdx-both@example.com")

		// Change user's region to pdx.
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "pdx", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		// pdx user must stay in pdx — their region requires matching.
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx, got %s", ec.region.Code)
		}
	})

	t.Run("pdx user gets error when only lax available", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
		}

		userID := createTestUser(t, server, "pdx-stuck@example.com")

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "pdx", userID)
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

	t.Run("lax user can use any open region exelet", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		fraRegion, _ := region.ByCode("fra")
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)
		fraExelet := &exeletClient{addr: "tcp://exelet-fra1-prod-01:9080", region: fraRegion}
		fraExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
			fraExelet.addr: fraExelet,
		}

		userID := createTestUser(t, server, "lax-both@example.com")

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		// lax user can get either lax or fra — neither requires matching.
		if ec.region.Code != "lax" && ec.region.Code != "fra" {
			t.Errorf("expected lax or fra, got %s", ec.region.Code)
		}
	})

	t.Run("affinity skips exelet in RequiresUserMatch region for non-matching user", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			laxExelet.addr: laxExelet,
		}

		// Create a lax user with a VM on the pdx exelet (simulating a pre-existing placement).
		userID := createTestUser(t, server, "lax-affinity@example.com")
		err := exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
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

		// Affinity would prefer pdx (has 1 VM there), but pdx.RequiresUserMatch
		// should block a lax user. Must fall through to lax.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lax" {
			t.Errorf("expected lax (affinity to pdx should be blocked), got %s", ec.region.Code)
		}
	})

	t.Run("affinity skips exelet when user RequiresUserMatch and exelet is different region", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
			pdxExelet.addr: pdxExelet,
		}

		// Create a pdx user with a VM on the lax exelet (pre-existing placement).
		userID := createTestUser(t, server, "pdx-affinity@example.com")
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "pdx", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}
		err = exedb.WithTx(server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
			_, err := q.InsertBox(ctx, exedb.InsertBoxParams{
				Ctrhost:         laxExelet.addr,
				Name:            "test-box-lax",
				Status:          "running",
				Image:           "test",
				CreatedByUserID: userID,
				Region:          "lax",
			})
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert box: %v", err)
		}

		// Affinity would prefer lax (has 1 VM there), but pdx user's
		// RequiresUserMatch should block placement outside pdx.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx (affinity to lax should be blocked for pdx user), got %s", ec.region.Code)
		}
	})

	t.Run("preferred exelet skipped when in RequiresUserMatch region for non-matching user", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)
		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			pdxExelet.addr: pdxExelet,
			laxExelet.addr: laxExelet,
		}

		// Set the preferred exelet to pdx.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, pdxExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		// lax user should not land on the pdx preferred exelet.
		userID := createTestUser(t, server, "lax-preferred@example.com")
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lax" {
			t.Errorf("expected lax (preferred pdx should be blocked), got %s", ec.region.Code)
		}
	})

	t.Run("user prefers own region over least loaded foreign region", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		lonRegion, _ := region.ByCode("lon")
		sgpRegion, _ := region.ByCode("sgp")

		lonExelet := &exeletClient{addr: "tcp://exelet-lon2-prod-01:9080", region: lonRegion}
		lonExelet.up.Store(true)
		sgpExelet := &exeletClient{addr: "tcp://exelet-sgp-prod-01:9080", region: sgpRegion}
		sgpExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			lonExelet.addr: lonExelet,
			sgpExelet.addr: sgpExelet,
		}

		// Create a lon user.
		userID := createTestUser(t, server, "lon-user@example.com")
		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "lon", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		// Both lon and sgp have RequiresUserMatch=false, so both are eligible.
		// But the user's own region (lon) should be preferred.
		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "lon" {
			t.Errorf("expected lon (user's own region should be preferred), got %s", ec.region.Code)
		}
	})

	t.Run("preferred exelet skipped when user RequiresUserMatch and preferred is different region", func(t *testing.T) {
		t.Parallel()
		server := newTestServer(t)
		ctx := context.Background()

		laxExelet := &exeletClient{addr: "tcp://exelet-lax1-prod-01:9080", region: laxRegion}
		laxExelet.up.Store(true)
		pdxExelet := &exeletClient{addr: "tcp://exelet-pdx1-prod-01:9080", region: pdxRegion}
		pdxExelet.up.Store(true)

		server.exeletClients = map[string]*exeletClient{
			laxExelet.addr: laxExelet,
			pdxExelet.addr: pdxExelet,
		}

		// Set the preferred exelet to lax.
		err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
			return q.SetPreferredExelet(ctx, laxExelet.addr)
		})
		if err != nil {
			t.Fatalf("failed to set preferred exelet: %v", err)
		}

		// pdx user should not land on the lax preferred exelet.
		userID := createTestUser(t, server, "pdx-preferred@example.com")
		err = server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`UPDATE users SET region = ? WHERE user_id = ?`, "pdx", userID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update user region: %v", err)
		}

		ec, _, err := server.selectExeletClient(ctx, userID)
		if err != nil {
			t.Fatalf("selectExeletClient: %v", err)
		}
		if ec.region.Code != "pdx" {
			t.Errorf("expected pdx (preferred lax should be blocked for pdx user), got %s", ec.region.Code)
		}
	})
}
