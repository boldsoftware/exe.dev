package exens

import (
	"context"
	"database/sql"
	"net"
	"net/netip"
	"path/filepath"
	"testing"

	"codeberg.org/miekg/dns"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

// newTestDB creates a test database with migrations applied.
func newTestDB(t *testing.T) *sqlite.DB {
	dbPath := filepath.Join(t.TempDir(), "exens_test.db")

	// Run migrations with raw DB
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		rawDB.Close()
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

	// Open with sqlite wrapper
	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("Failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNSRecords(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	// Test production config
	t.Run("Prod", func(t *testing.T) {
		server := NewServer(db, log, "exe.xyz", "exe.dev")

		rrs, err := server.lookupNS(ctx, "exe.xyz", "exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 2 {
			t.Fatalf("expected 2 NS records, got %d", len(rrs))
		}

		// Check ns1 and ns2
		ns1, ok := rrs[0].(*dns.NS)
		if !ok {
			t.Fatalf("expected *dns.NS, got %T", rrs[0])
		}
		if ns1.Ns != "ns1.exe.dev." {
			t.Errorf("expected ns1.exe.dev., got %s", ns1.Ns)
		}

		ns2, ok := rrs[1].(*dns.NS)
		if !ok {
			t.Fatalf("expected *dns.NS, got %T", rrs[1])
		}
		if ns2.Ns != "ns2.exe.dev." {
			t.Errorf("expected ns2.exe.dev., got %s", ns2.Ns)
		}
	})

	// Test staging config
	t.Run("Staging", func(t *testing.T) {
		server := NewServer(db, log, "exe-staging.xyz", "exe-staging.dev")

		rrs, err := server.lookupNS(ctx, "exe-staging.xyz", "exe-staging.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 2 {
			t.Fatalf("expected 2 NS records, got %d", len(rrs))
		}

		ns1 := rrs[0].(*dns.NS)
		if ns1.Ns != "ns1.exe-staging.dev." {
			t.Errorf("expected ns1.exe-staging.dev., got %s", ns1.Ns)
		}
	})

	// Test non-matching domain returns nothing
	t.Run("WrongDomain", func(t *testing.T) {
		server := NewServer(db, log, "exe.xyz", "exe.dev")

		rrs, err := server.lookupNS(ctx, "other.com", "other.com.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 0 {
			t.Errorf("expected 0 records for wrong domain, got %d", len(rrs))
		}
	})
}

func TestSOARecords(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	// Test production config
	t.Run("Prod", func(t *testing.T) {
		server := NewServer(db, log, "exe.xyz", "exe.dev")

		rrs, err := server.lookupSOA(ctx, "exe.xyz", "exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 SOA record, got %d", len(rrs))
		}

		soa, ok := rrs[0].(*dns.SOA)
		if !ok {
			t.Fatalf("expected *dns.SOA, got %T", rrs[0])
		}
		if soa.Ns != "ns1.exe.dev." {
			t.Errorf("expected MNAME ns1.exe.dev., got %s", soa.Ns)
		}
		if soa.Mbox != "hostmaster.exe.dev." {
			t.Errorf("expected RNAME hostmaster.exe.dev., got %s", soa.Mbox)
		}
		if soa.Minttl != 300 {
			t.Errorf("expected minimum TTL 300, got %d", soa.Minttl)
		}
	})

	// Test staging config
	t.Run("Staging", func(t *testing.T) {
		server := NewServer(db, log, "exe-staging.xyz", "exe-staging.dev")

		rrs, err := server.lookupSOA(ctx, "exe-staging.xyz", "exe-staging.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 SOA record, got %d", len(rrs))
		}

		soa := rrs[0].(*dns.SOA)
		if soa.Ns != "ns1.exe-staging.dev." {
			t.Errorf("expected MNAME ns1.exe-staging.dev., got %s", soa.Ns)
		}
		if soa.Mbox != "hostmaster.exe-staging.dev." {
			t.Errorf("expected RNAME hostmaster.exe-staging.dev., got %s", soa.Mbox)
		}
	})

	// Test non-matching domain returns nothing
	t.Run("WrongDomain", func(t *testing.T) {
		server := NewServer(db, log, "exe.xyz", "exe.dev")

		rrs, err := server.lookupSOA(ctx, "other.com", "other.com.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 0 {
			t.Errorf("expected 0 records for wrong domain, got %d", len(rrs))
		}
	})
}

func TestXtermWildcardA(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	// Add shard 1 IP (for box resolution, not used for xterm/shelley/apex)
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
			Shard:    1,
			PublicIp: "10.0.0.1",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	lobbyIP := netip.MustParseAddr("10.0.0.100")
	server := NewServer(db, log, "exe.xyz", "exe.dev")
	server.SetLobbyIP(lobbyIP)

	// Test wildcard xterm subdomain
	t.Run("WildcardXterm", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "anything.xterm.exe.xyz", "anything.xterm.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 A record, got %d", len(rrs))
		}

		a, ok := rrs[0].(*dns.A)
		if !ok {
			t.Fatalf("expected *dns.A, got %T", rrs[0])
		}
		if a.A.String() != "10.0.0.100" {
			t.Errorf("expected 10.0.0.100 (lobby IP), got %s", a.A.String())
		}
	})

	// Test multiple levels of subdomain
	t.Run("DeepWildcardXterm", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "foo.bar.xterm.exe.xyz", "foo.bar.xterm.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 A record for deep wildcard, got %d", len(rrs))
		}

		a := rrs[0].(*dns.A)
		if a.A.String() != "10.0.0.100" {
			t.Errorf("expected 10.0.0.100 (lobby IP), got %s", a.A.String())
		}
	})

	// Test wildcard shelley subdomain
	t.Run("WildcardShelley", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "mybox.shelley.exe.xyz", "mybox.shelley.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 A record for shelley wildcard, got %d", len(rrs))
		}

		a, ok := rrs[0].(*dns.A)
		if !ok {
			t.Fatalf("expected *dns.A, got %T", rrs[0])
		}
		if a.A.String() != "10.0.0.100" {
			t.Errorf("expected 10.0.0.100 (lobby IP), got %s", a.A.String())
		}
	})

	// Test staging domain
	t.Run("StagingXterm", func(t *testing.T) {
		stagingServer := NewServer(db, log, "exe-staging.xyz", "exe-staging.dev")
		stagingServer.SetLobbyIP(lobbyIP)

		rrs, err := stagingServer.lookupA(ctx, "test.xterm.exe-staging.xyz", "test.xterm.exe-staging.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 A record for staging xterm, got %d", len(rrs))
		}
	})

	// Test base domain A record (exe.xyz -> lobby IP)
	t.Run("BaseDomain", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "exe.xyz", "exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 A record for base domain, got %d", len(rrs))
		}

		a, ok := rrs[0].(*dns.A)
		if !ok {
			t.Fatalf("expected *dns.A, got %T", rrs[0])
		}
		if a.A.String() != "10.0.0.100" {
			t.Errorf("expected 10.0.0.100 (lobby IP), got %s", a.A.String())
		}
	})
}

func TestDNSServer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	// Add test data: ip_shards and boxes
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		// Add ip_shard
		if err := queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
			Shard:    1,
			PublicIp: "1.2.3.4",
		}); err != nil {
			return err
		}

		// Add a user (required for box)
		if err := queries.InsertUser(ctx, exedb.InsertUserParams{
			UserID: "test-user",
			Email:  "test@example.com",
		}); err != nil {
			return err
		}

		// Add a box
		boxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Name:            "testbox",
			Status:          "running",
			Image:           "ubuntu",
			Ctrhost:         "localhost",
			CreatedByUserID: "test-user",
		})
		if err != nil {
			return err
		}

		// Add box_ip_shard mapping
		return queries.InsertBoxIPShard(ctx, exedb.InsertBoxIPShardParams{
			BoxID:   int(boxID),
			UserID:  "test-user",
			IPShard: 1,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(db, log, "exe.xyz", "exe.dev")

	// Add a TXT record
	server.SetTXTRecord("_acme-challenge.exe.xyz", "test-token-123")

	t.Run("LookupARecord", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "s001.exe.xyz", "s001.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 record, got %d", len(rrs))
		}
		a, ok := rrs[0].(*dns.A)
		if !ok {
			t.Fatalf("expected *dns.A, got %T", rrs[0])
		}
		if a.A.String() != "1.2.3.4" {
			t.Errorf("expected 1.2.3.4, got %s", a.A.String())
		}
	})

	t.Run("LookupCNAMERecord", func(t *testing.T) {
		rrs, err := server.lookupCNAME(ctx, "testbox.exe.xyz", "testbox.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 record, got %d", len(rrs))
		}
		cname, ok := rrs[0].(*dns.CNAME)
		if !ok {
			t.Fatalf("expected *dns.CNAME, got %T", rrs[0])
		}
		if cname.Target != "s001.exe.xyz." {
			t.Errorf("expected s001.exe.xyz., got %s", cname.Target)
		}
	})

	t.Run("LookupARecordForBoxName", func(t *testing.T) {
		// When querying A record for a box name, should get CNAME + A record
		rrs, err := server.lookupA(ctx, "testbox.exe.xyz", "testbox.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 2 {
			t.Fatalf("expected 2 records (CNAME + A), got %d", len(rrs))
		}
		cname, ok := rrs[0].(*dns.CNAME)
		if !ok {
			t.Fatalf("expected first record to be *dns.CNAME, got %T", rrs[0])
		}
		if cname.Target != "s001.exe.xyz." {
			t.Errorf("expected CNAME target s001.exe.xyz., got %s", cname.Target)
		}
		a, ok := rrs[1].(*dns.A)
		if !ok {
			t.Fatalf("expected second record to be *dns.A, got %T", rrs[1])
		}
		if a.A.String() != "1.2.3.4" {
			t.Errorf("expected A record 1.2.3.4, got %s", a.A.String())
		}
	})

	t.Run("LookupTXTRecord", func(t *testing.T) {
		rrs, err := server.lookupTXT(ctx, "_acme-challenge.exe.xyz", "_acme-challenge.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 1 {
			t.Fatalf("expected 1 record, got %d", len(rrs))
		}
		txt, ok := rrs[0].(*dns.TXT)
		if !ok {
			t.Fatalf("expected *dns.TXT, got %T", rrs[0])
		}
		if len(txt.Txt) != 1 || txt.Txt[0] != "test-token-123" {
			t.Errorf("expected [test-token-123], got %v", txt.Txt)
		}
	})

	t.Run("LookupNonexistentA", func(t *testing.T) {
		rrs, err := server.lookupA(ctx, "s099.exe.xyz", "s099.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 0 {
			t.Errorf("expected 0 records, got %d", len(rrs))
		}
	})

	t.Run("LookupNonexistentCNAME", func(t *testing.T) {
		rrs, err := server.lookupCNAME(ctx, "nonexistent.exe.xyz", "nonexistent.exe.xyz.", dns.ClassINET)
		if err != nil {
			t.Fatal(err)
		}
		if len(rrs) != 0 {
			t.Errorf("expected 0 records, got %d", len(rrs))
		}
	})
}

func TestTXTRecordManagement(t *testing.T) {
	db := newTestDB(t)
	log := tslog.Slogger(t)
	server := NewServer(db, log, "exe.xyz", "exe.dev")

	// Add TXT records
	server.SetTXTRecord("_acme.example.com", "token1")
	server.SetTXTRecord("_acme.example.com", "token2")

	// Verify
	values := server.GetTXTRecords("_acme.example.com")
	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}

	// Delete one
	server.DeleteTXTRecord("_acme.example.com", "token1")
	values = server.GetTXTRecords("_acme.example.com")
	if len(values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(values))
	}
	if values[0] != "token2" {
		t.Errorf("expected token2, got %s", values[0])
	}

	// Delete the other
	server.DeleteTXTRecord("_acme.example.com", "token2")
	values = server.GetTXTRecords("_acme.example.com")
	if len(values) != 0 {
		t.Errorf("expected 0 values, got %d", len(values))
	}
}

func TestParseShardFromName(t *testing.T) {
	tests := []struct {
		name    string
		want    int
		wantErr bool
	}{
		{"s001.exe.xyz", 1, false},
		{"s025.exe.xyz", 25, false},
		{"s010.exe.dev", 10, false},
		{"s000.exe.xyz", 0, true}, // Invalid shard
		{"s026.exe.xyz", 0, true}, // Out of range
		{"s1.exe.xyz", 0, true},   // Wrong format
		{"testbox.exe.xyz", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseShardFromName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseShardFromName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseShardFromName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestServerStartStop(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	server := NewServer(db, log, "exe.xyz", "exe.dev")

	// Test that Start with no IPs returns an error
	err := server.Start(ctx, nil)
	if err == nil {
		t.Error("expected error when starting with no IPs")
	}

	// Test that Stop works even if not started
	server.Stop(ctx)
}

// TestDNSServerIntegration tests the DNS server with actual DNS queries.
func TestDNSServerIntegration(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	log := tslog.Slogger(t)

	// Add test data
	err := db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
			Shard:    1,
			PublicIp: "192.168.1.1",
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start a custom DNS server on a high port for testing
	mux := dns.NewServeMux()
	server := NewServer(db, log, "exe.xyz", "exe.dev")
	server.SetLobbyIP(netip.MustParseAddr("192.168.0.1")) // lobby IP for testing
	mux.HandleFunc(".", server.handleDNS)

	// Find an available port
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.LocalAddr().String()

	ready := make(chan struct{})
	dnsServer := &dns.Server{
		PacketConn:        ln,
		Net:               "udp",
		Handler:           mux,
		NotifyStartedFunc: func(context.Context) { close(ready) },
	}

	go dnsServer.ActivateAndServe()
	<-ready
	defer dnsServer.Shutdown(ctx)

	client := dns.NewClient()

	t.Run("QueryARecord", func(t *testing.T) {
		msg := dns.NewMsg("s001.exe.xyz.", dns.TypeA)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
		}

		a, ok := resp.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", resp.Answer[0])
		}

		if a.A.String() != "192.168.1.1" {
			t.Errorf("expected 192.168.1.1, got %s", a.A.String())
		}
	})

	t.Run("QueryNonexistent", func(t *testing.T) {
		msg := dns.NewMsg("nonexistent.exe.xyz.", dns.TypeA)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if resp.Rcode != dns.RcodeNameError {
			t.Errorf("expected NXDOMAIN, got %s", dns.RcodeToString[resp.Rcode])
		}
	})

	t.Run("QueryTXTRecord", func(t *testing.T) {
		// Add a TXT record
		server.SetTXTRecord("_test.exe.xyz", "test-value")

		msg := dns.NewMsg("_test.exe.xyz.", dns.TypeTXT)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
		}

		txt, ok := resp.Answer[0].(*dns.TXT)
		if !ok {
			t.Fatalf("expected TXT record, got %T", resp.Answer[0])
		}

		if len(txt.Txt) != 1 || txt.Txt[0] != "test-value" {
			t.Errorf("expected [test-value], got %v", txt.Txt)
		}
	})

	t.Run("QueryNSRecord", func(t *testing.T) {
		msg := dns.NewMsg("exe.xyz.", dns.TypeNS)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 2 {
			t.Fatalf("expected 2 NS answers, got %d", len(resp.Answer))
		}

		ns1, ok := resp.Answer[0].(*dns.NS)
		if !ok {
			t.Fatalf("expected NS record, got %T", resp.Answer[0])
		}
		if ns1.Ns != "ns1.exe.dev." {
			t.Errorf("expected ns1.exe.dev., got %s", ns1.Ns)
		}
	})

	t.Run("QueryXtermWildcard", func(t *testing.T) {
		msg := dns.NewMsg("something.xterm.exe.xyz.", dns.TypeA)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 A answer for xterm wildcard, got %d", len(resp.Answer))
		}

		a, ok := resp.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", resp.Answer[0])
		}
		// Should return lobby IP (192.168.0.1 from test setup)
		if a.A.String() != "192.168.0.1" {
			t.Errorf("expected 192.168.0.1, got %s", a.A.String())
		}
	})

	t.Run("QueryBaseDomain", func(t *testing.T) {
		msg := dns.NewMsg("exe.xyz.", dns.TypeA)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 A answer for base domain, got %d", len(resp.Answer))
		}

		a, ok := resp.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", resp.Answer[0])
		}
		// Should return lobby IP (192.168.0.1 from test setup)
		if a.A.String() != "192.168.0.1" {
			t.Errorf("expected 192.168.0.1, got %s", a.A.String())
		}
	})

	t.Run("QuerySOARecord", func(t *testing.T) {
		msg := dns.NewMsg("exe.xyz.", dns.TypeSOA)

		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatal(err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 SOA answer, got %d", len(resp.Answer))
		}

		soa, ok := resp.Answer[0].(*dns.SOA)
		if !ok {
			t.Fatalf("expected SOA record, got %T", resp.Answer[0])
		}
		if soa.Ns != "ns1.exe.dev." {
			t.Errorf("expected ns1.exe.dev., got %s", soa.Ns)
		}
	})
}
