package region

import (
	"strings"
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
		// CI runner (bridge network)
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
		// Exelet hosts with region+datacenter number
		{"exelet-lax2-staging-01", "lax", false},
		{"exelet-lax2-staging-01:9080", "lax", false},
		{"tcp://exelet-lax2-staging-01:9080", "lax", false},
		{"exelet-pdx1-prod-01", "pdx", false},
		{"exelet-fra3-prod-02:9080", "fra", false},
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

func TestForUser(t *testing.T) {
	tests := []struct {
		name    string
		country string
		lat     float64
		lon     float64
		want    string
	}{
		// US east/west split at -100° longitude
		{"US west coast", "US", 34.05, -118.24, "lax"},    // Los Angeles
		{"US east coast", "US", 40.71, -74.01, "nyc"},     // New York
		{"US midwest east", "US", 41.88, -87.63, "nyc"},   // Chicago (east of -100)
		{"US mountain west", "US", 39.74, -104.99, "lax"}, // Denver (west of -100)
		{"US no coords", "US", 0, 0, "lax"},               // no coords → lax (non-sticky)

		// Country code mapping — Europe
		{"Germany", "DE", 0, 0, "fra"},
		{"France", "FR", 0, 0, "fra"},
		{"UK", "GB", 0, 0, "lon"},
		{"Ireland", "IE", 0, 0, "lon"},
		{"Sweden", "SE", 0, 0, "fra"},
		{"Poland", "PL", 0, 0, "fra"},

		// Country code mapping — Asia
		{"Japan", "JP", 0, 0, "tyo"},
		{"India", "IN", 0, 0, "tyo"},
		{"Singapore", "SG", 0, 0, "tyo"},
		{"South Korea", "KR", 0, 0, "tyo"},

		// Canada east/west split (same as US)
		{"Vancouver", "CA", 49.28, -123.12, "lax"},
		{"Toronto", "CA", 43.65, -79.38, "nyc"},
		{"Canada no coords", "CA", 0, 0, "lax"},
		{"Mexico", "MX", 0, 0, "lax"},
		{"Brazil", "BR", 0, 0, "nyc"},

		// Country code mapping — Oceania
		{"Australia", "AU", 0, 0, "syd"},
		{"New Zealand", "NZ", 0, 0, "syd"},

		// Country code mapping — Africa
		{"South Africa", "ZA", 0, 0, "fra"},
		{"Egypt", "EG", 0, 0, "fra"},

		// Middle East
		{"Israel", "IL", 0, 0, "fra"},
		{"UAE", "AE", 0, 0, "fra"},

		// Case insensitive
		{"lowercase country", "gb", 0, 0, "lon"},

		// Unmapped country with coordinates → nearest
		{"Fiji with coords", "FJ", -17.71, 177.97, "syd"},
		{"unmapped country near Reykjavík", "XX", 64.15, -21.94, "lon"},

		// No country, no coords → default
		{"empty everything", "", 0, 0, "pdx"},

		// No country, has coords → nearest
		{"coords only mid-Atlantic", "", 0, -30, "nyc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ForUser(tt.country, tt.lat, tt.lon)
			if got.Code != tt.want {
				t.Errorf("ForUser(%q, %v, %v) = %q, want %q", tt.country, tt.lat, tt.lon, got.Code, tt.want)
			}
		})
	}
}

func TestCountryMapOnlyActiveRegions(t *testing.T) {
	for cc, r := range countryToRegion {
		if !r.Active {
			t.Errorf("countryToRegion[%q] maps to inactive region %q", cc, r.Code)
		}
	}
}

func TestCountryMapKeysUppercase(t *testing.T) {
	for cc := range countryToRegion {
		if cc != strings.ToUpper(cc) {
			t.Errorf("countryToRegion key %q is not uppercase", cc)
		}
	}
}
