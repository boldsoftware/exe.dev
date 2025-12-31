package e1e

import (
	"context"
	"net"
	"testing"

	"codeberg.org/miekg/dns"
	"exe.dev/exedb"
	"exe.dev/exens"
	"exe.dev/sqlite"
	"exe.dev/tslog"
)

// TestEmbeddedDNS tests the embedded DNS server with a real VM.
// It creates a VM, then spins up an exens.Server against the test database
// and verifies that DNS queries return the expected records.
func TestEmbeddedDNS(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Create a user and a box
	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()
	waitForSSH(t, boxName, keyFile)

	// Open the test database that exed is using
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Populate ip_shards table with test data (shard 1 -> test IP)
	// In production this is populated from EC2 metadata
	testIP := "10.20.30.40"
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpsertIPShard(ctx, exedb.UpsertIPShardParams{
			Shard:    1,
			PublicIp: testIP,
		})
	})
	if err != nil {
		t.Fatalf("failed to insert ip_shard: %v", err)
	}

	// Create an exens.Server against the test database
	log := tslog.Slogger(t)
	server := exens.NewServer(db, log)

	// Start DNS server on a random high port
	mux := dns.NewServeMux()
	mux.HandleFunc(".", server.Handler())

	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.LocalAddr().String()

	dnsServer := &dns.Server{
		PacketConn: ln,
		Net:        "udp",
		Handler:    mux,
	}

	go dnsServer.ActivateAndServe()
	defer dnsServer.Shutdown(ctx)

	// Create DNS client
	// No sleep needed - the socket is already bound via ListenPacket,
	// ActivateAndServe just starts processing packets on it.
	client := dns.NewClient()

	// Test CNAME lookup for the box
	t.Run("CNAME", func(t *testing.T) {
		msg := dns.NewMsg(boxName+".exe.cloud.", dns.TypeCNAME)
		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatalf("DNS query failed: %v", err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
		}

		cname, ok := resp.Answer[0].(*dns.CNAME)
		if !ok {
			t.Fatalf("expected CNAME record, got %T", resp.Answer[0])
		}

		// The box should be on shard 1 (only shard we populated)
		expected := "s001.exe.cloud."
		if cname.Target != expected {
			t.Errorf("expected CNAME target %s, got %s", expected, cname.Target)
		}
	})

	// Test A record lookup for the shard
	t.Run("A", func(t *testing.T) {
		msg := dns.NewMsg("s001.exe.cloud.", dns.TypeA)
		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatalf("DNS query failed: %v", err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
		}

		a, ok := resp.Answer[0].(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", resp.Answer[0])
		}

		if a.A.String() != testIP {
			t.Errorf("expected IP %s, got %s", testIP, a.A.String())
		}
	})

	// Test TXT record (ACME challenge simulation)
	t.Run("TXT", func(t *testing.T) {
		challengeName := "_acme-challenge.exe.cloud"
		challengeValue := "test-acme-token-12345"

		server.SetTXTRecord(challengeName, challengeValue)

		msg := dns.NewMsg(challengeName+".", dns.TypeTXT)
		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatalf("DNS query failed: %v", err)
		}

		if len(resp.Answer) != 1 {
			t.Fatalf("expected 1 answer, got %d", len(resp.Answer))
		}

		txt, ok := resp.Answer[0].(*dns.TXT)
		if !ok {
			t.Fatalf("expected TXT record, got %T", resp.Answer[0])
		}

		if len(txt.Txt) != 1 || txt.Txt[0] != challengeValue {
			t.Errorf("expected TXT value [%s], got %v", challengeValue, txt.Txt)
		}

		// Clean up
		server.DeleteTXTRecord(challengeName, challengeValue)
	})

	// Test NXDOMAIN for nonexistent box
	t.Run("NXDOMAIN", func(t *testing.T) {
		msg := dns.NewMsg("nonexistent-box.exe.cloud.", dns.TypeCNAME)
		resp, _, err := client.Exchange(ctx, msg, "udp", addr)
		if err != nil {
			t.Fatalf("DNS query failed: %v", err)
		}

		if resp.Rcode != dns.RcodeNameError {
			t.Errorf("expected NXDOMAIN, got %s", dns.RcodeToString[resp.Rcode])
		}
	})

	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.disconnect()
}
