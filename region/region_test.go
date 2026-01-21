package region

import (
	"testing"
)

func TestByCode(t *testing.T) {
	tests := []struct {
		code    string
		want    string
		wantErr bool
	}{
		{"pdx", "pdx", false},
		{"PDX", "pdx", false}, // case insensitive
		{"lax", "lax", false},
		{"nyc", "nyc", false},
		{"fra", "fra", false},
		{"tyo", "tyo", false},
		{"syd", "syd", false},
		{"dev", "dev", false},
		{"ci", "ci", false},
		{"unknown", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got, err := ByCode(tt.code)
			if (err != nil) != tt.wantErr {
				t.Errorf("ByCode(%q) error = %v, wantErr %v", tt.code, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.Code != tt.want {
				t.Errorf("ByCode(%q).Code = %q, want %q", tt.code, got.Code, tt.want)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	def := Default()
	if def.Code != "pdx" {
		t.Errorf("Default().Code = %q, want %q", def.Code, "pdx")
	}
}

func TestAll(t *testing.T) {
	all := All()
	if len(all) != 8 {
		t.Errorf("All() returned %d regions, want 8", len(all))
	}
	// Check that pdx is first (default region)
	if all[0].Code != "pdx" {
		t.Errorf("All()[0].Code = %q, want pdx", all[0].Code)
	}
}

func TestParseExeletRegion(t *testing.T) {
	tests := []struct {
		host    string
		want    string
		wantErr bool
	}{
		// Legacy AWS hosts
		{"exe-ctr-01:8080", "pdx", false},
		{"exe-ctr-01", "pdx", false},
		{"exe-ctr-02:8080", "pdx", false},
		// Local development
		{"lima-vm", "dev", false},
		{"lima-vm:8080", "dev", false},
		{"lima-exe-ctr.local", "dev", false},
		{"tcp://lima-exe-ctr.local:9080", "dev", false},
		{"localhost:8080", "dev", false},
		{"localhost", "dev", false},
		{"127.0.0.1:8080", "dev", false},
		{"127.0.0.1", "dev", false},
		// CI runner (libvirt default network)
		{"ubuntu@192.168.122.14", "ci", false},
		{"ubuntu@192.168.122.14:8080", "ci", false},
		{"tcp://ubuntu@192.168.122.14:44037", "ci", false},
		// New convention: -REGION- marker
		{"ctr-pdx-prod-01:8080", "pdx", false},
		{"ctr-lax-prod-01:8080", "lax", false},
		{"ctr-lax-prod-01", "lax", false},
		{"ctr-dev-prod-01", "dev", false},
		{"ctr-nyc-prod-01", "nyc", false},
		{"ctr-fra-prod-01", "fra", false},
		{"ctr-tyo-prod-01", "tyo", false},
		{"ctr-syd-prod-01", "syd", false},
		{"ctr-ci-prod-01", "ci", false},
		{"tcp://ctr-lax-prod-01:9080", "lax", false},
		// Different prefixes work too
		{"foo-pdx-bar", "pdx", false},
		{"host-syd-123", "syd", false},
		// Errors
		{"unknown-host", "", true},
		{"ctr-unknown-prod-01", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got, err := ParseExeletRegion(tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExeletRegion(%q) error = %v, wantErr %v", tt.host, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got.Code != tt.want {
				t.Errorf("ParseExeletRegion(%q).Code = %q, want %q", tt.host, got.Code, tt.want)
			}
		})
	}
}
