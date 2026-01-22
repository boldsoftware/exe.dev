package replication

import (
	"testing"
)

func TestParseTargetConfig(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		wantType  string
		wantUser  string
		wantHost  string
		wantPort  string
		wantPool  string
		wantPath  string
		wantErr   bool
	}{
		{
			name:      "valid SSH target",
			targetURL: "ssh://backup@nas.local/tank",
			wantType:  "ssh",
			wantUser:  "backup",
			wantHost:  "nas.local",
			wantPool:  "tank",
		},
		{
			name:      "SSH target with port",
			targetURL: "ssh://backup@nas.local:22/tank/backups",
			wantType:  "ssh",
			wantUser:  "backup",
			wantHost:  "nas.local",
			wantPort:  "22",
			wantPool:  "tank/backups",
		},
		{
			name:      "valid file target",
			targetURL: "file:///var/backups/exelet",
			wantType:  "file",
			wantPath:  "/var/backups/exelet",
		},
		{
			name:      "file target with nested path",
			targetURL: "file:///mnt/backup/exelet/snapshots",
			wantType:  "file",
			wantPath:  "/mnt/backup/exelet/snapshots",
		},
		{
			name:      "SSH target missing user",
			targetURL: "ssh://nas.local/tank",
			wantErr:   true,
		},
		{
			name:      "SSH target missing pool",
			targetURL: "ssh://backup@nas.local",
			wantErr:   true,
		},
		{
			name:      "file target missing path",
			targetURL: "file://",
			wantErr:   true,
		},
		{
			name:      "unsupported scheme",
			targetURL: "http://example.com/backup",
			wantErr:   true,
		},
		{
			name:      "invalid URL",
			targetURL: "://invalid",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseTargetConfig(tt.targetURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseTargetConfig() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseTargetConfig() unexpected error: %v", err)
				return
			}

			if cfg.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", cfg.Type, tt.wantType)
			}

			switch tt.wantType {
			case "ssh":
				if cfg.User != tt.wantUser {
					t.Errorf("User = %q, want %q", cfg.User, tt.wantUser)
				}
				if cfg.Host != tt.wantHost {
					t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
				}
				if cfg.Port != tt.wantPort {
					t.Errorf("Port = %q, want %q", cfg.Port, tt.wantPort)
				}
				if cfg.Pool != tt.wantPool {
					t.Errorf("Pool = %q, want %q", cfg.Pool, tt.wantPool)
				}
			case "file":
				if cfg.Path != tt.wantPath {
					t.Errorf("Path = %q, want %q", cfg.Path, tt.wantPath)
				}
			}
		})
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		limit string
		want  int64
	}{
		{"", 0},
		{"100", 100},
		{"100K", 100 * 1024},
		{"100k", 100 * 1024},
		{"10M", 10 * 1024 * 1024},
		{"10m", 10 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
		{"  500M  ", 500 * 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.limit, func(t *testing.T) {
			got := parseRateLimit(tt.limit)
			if got != tt.want {
				t.Errorf("parseRateLimit(%q) = %d, want %d", tt.limit, got, tt.want)
			}
		})
	}
}

func TestParseHumanSize(t *testing.T) {
	tests := []struct {
		size string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"1234", 1234},
		{"1K", 1024},
		{"1k", 1024},
		{"100K", 100 * 1024},
		{"1M", 1024 * 1024},
		{"1m", 1024 * 1024},
		{"500M", 500 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
		{"2.5G", int64(2.5 * 1024 * 1024 * 1024)},
		{"1.5M", int64(1.5 * 1024 * 1024)},
		{"1T", 1024 * 1024 * 1024 * 1024},
		{"  100M  ", 100 * 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			got := parseHumanSize(tt.size)
			if got != tt.want {
				t.Errorf("parseHumanSize(%q) = %d, want %d", tt.size, got, tt.want)
			}
		})
	}
}
