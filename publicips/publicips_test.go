package publicips

import (
	"context"
	"net/netip"
	"testing"
)

func TestLocalhostIPs(t *testing.T) {
	const numShards = 25
	ips, err := LocalhostIPs(context.Background(), "exe.cloud", numShards)
	if err != nil {
		t.Fatalf("LocalhostIPs failed: %v", err)
	}

	if len(ips) != numShards {
		t.Errorf("DevIPs returned %d entries, want %d", len(ips), numShards)
	}

	// Check specific entries
	tests := []struct {
		ip      string
		wantIP  string
		wantDom string
		wantSh  int
	}{
		{"127.21.0.1", "127.21.0.1", "na001.exe.cloud", 1},
		{"127.21.0.7", "127.21.0.7", "na007.exe.cloud", 7},
		{"127.21.0.25", "127.21.0.25", "na025.exe.cloud", 25},
	}

	for _, tt := range tests {
		addr := netip.MustParseAddr(tt.ip)
		info, ok := ips[addr]
		if !ok {
			t.Errorf("DevIPs[%s] not found", tt.ip)
			continue
		}
		if info.IP.String() != tt.wantIP {
			t.Errorf("DevIPs[%s].IP = %s, want %s", tt.ip, info.IP, tt.wantIP)
		}
		if info.Domain != tt.wantDom {
			t.Errorf("DevIPs[%s].Domain = %s, want %s", tt.ip, info.Domain, tt.wantDom)
		}
		if info.Shard != tt.wantSh {
			t.Errorf("DevIPs[%s].Shard = %d, want %d", tt.ip, info.Shard, tt.wantSh)
		}
	}

	// Should not have invalid entries
	invalidIPs := []string{
		"127.21.0.0",  // shard 0 invalid
		"127.21.0.26", // shard > 25 invalid
		"127.0.0.1",   // not 127.21.0.X
		"10.0.0.1",    // private IP, not dev
	}
	for _, ip := range invalidIPs {
		addr := netip.MustParseAddr(ip)
		if _, ok := ips[addr]; ok {
			t.Errorf("DevIPs should not contain %s", ip)
		}
	}
}

func TestLocalhostIPsLargeShardCount(t *testing.T) {
	const numShards = 300
	ips, err := LocalhostIPs(context.Background(), "exe.cloud", numShards)
	if err != nil {
		t.Fatalf("LocalhostIPs failed: %v", err)
	}

	if len(ips) != numShards {
		t.Fatalf("got %d entries, want %d", len(ips), numShards)
	}

	// Shard 1 should still be 127.21.0.1
	addr1 := netip.MustParseAddr("127.21.0.1")
	if info, ok := ips[addr1]; !ok || info.Shard != 1 {
		t.Errorf("shard 1: got %+v, ok=%v", info, ok)
	}

	// Shard 254 should be 127.21.0.254
	addr254 := netip.MustParseAddr("127.21.0.254")
	if info, ok := ips[addr254]; !ok || info.Shard != 254 {
		t.Errorf("shard 254: got %+v, ok=%v", info, ok)
	}

	// Shard 255 should wrap to 127.21.1.1
	addr255 := netip.MustParseAddr("127.21.1.1")
	if info, ok := ips[addr255]; !ok || info.Shard != 255 {
		t.Errorf("shard 255: got %+v, ok=%v", info, ok)
	}

	// Shard 300 should be 127.21.1.46 ((300-1)/254=1, (300-1)%254+1=46)
	addr300 := netip.MustParseAddr("127.21.1.46")
	if info, ok := ips[addr300]; !ok || info.Shard != 300 {
		t.Errorf("shard 300: got %+v, ok=%v", info, ok)
	}

	// All IPs should be distinct
	seen := make(map[netip.Addr]int)
	for addr, info := range ips {
		if prev, dup := seen[addr]; dup {
			t.Fatalf("duplicate IP %s for shards %d and %d", addr, prev, info.Shard)
		}
		seen[addr] = info.Shard
	}
}
