package replication

import (
	"testing"
)

func TestParseZpoolTargetConfig(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantPool string
		wantErr  bool
	}{
		{
			name:     "simple pool",
			url:      "zpool:///backup",
			wantPool: "backup",
		},
		{
			name:     "nested pool path",
			url:      "zpool:///backup/replication",
			wantPool: "backup/replication",
		},
		{
			name:    "missing pool name",
			url:     "zpool:///",
			wantErr: true,
		},
		{
			name:    "empty path",
			url:     "zpool://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseTargetConfig(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseTargetConfig(%q) expected error, got nil", tt.url)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseTargetConfig(%q) unexpected error: %v", tt.url, err)
				return
			}
			if cfg.Type != "zpool" {
				t.Errorf("Type = %q, want %q", cfg.Type, "zpool")
			}
			if cfg.Pool != tt.wantPool {
				t.Errorf("Pool = %q, want %q", cfg.Pool, tt.wantPool)
			}
		})
	}
}

func TestParseTargetZpool(t *testing.T) {
	target, err := ParseTarget("zpool:///backup", "", "", "", "50M")
	if err != nil {
		t.Fatalf("ParseTarget() unexpected error: %v", err)
	}
	zt, ok := target.(*ZpoolTarget)
	if !ok {
		t.Fatalf("expected *ZpoolTarget, got %T", target)
	}
	if zt.Type() != "zpool" {
		t.Errorf("Type() = %q, want %q", zt.Type(), "zpool")
	}
	if zt.config.Pool != "backup" {
		t.Errorf("Pool = %q, want %q", zt.config.Pool, "backup")
	}
	if zt.config.BandwidthLimit != "50M" {
		t.Errorf("BandwidthLimit = %q, want %q", zt.config.BandwidthLimit, "50M")
	}
}

func TestZpoolTargetDataset(t *testing.T) {
	zt := &ZpoolTarget{config: &TargetConfig{Pool: "backup"}}
	got := zt.dataset("vm000001-blue-falcon")
	want := "backup/vm000001-blue-falcon"
	if got != want {
		t.Errorf("dataset() = %q, want %q", got, want)
	}
}
