package exens

import (
	"context"
	"database/sql"
	"net"
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

	server := NewServer(db, log)

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
	server := NewServer(db, log)

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
		{"s000.exe.xyz", 0, true},  // Invalid shard
		{"s026.exe.xyz", 0, true},  // Out of range
		{"s1.exe.xyz", 0, true},    // Wrong format
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

	server := NewServer(db, log)

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
	server := NewServer(db, log)
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
}
