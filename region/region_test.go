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
	if def.Code != "lax" {
		t.Errorf("Default().Code = %q, want %q", def.Code, "lax")
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
		{"India", "IN", 0, 0, "sgp"},
		{"Singapore", "SG", 0, 0, "sgp"},
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
		// Gated regions (RequiresUserMatch) must never be picked by nearest,
		// even for users whose coordinates land right on top of them.
		{"unmapped country near PDX", "XX", 45.59, -122.60, "lax"},
		{"unmapped country near IAD", "XX", 38.95, -77.45, "nyc"},

		// No country, no coords → default
		{"empty everything", "", 0, 0, "lax"},

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

func TestAvailableForUnlocked(t *testing.T) {
	// A lax user with iad unlocked (e.g. via team exelet) should see iad.
	got := AvailableFor("lax", "iad")
	codes := make(map[string]bool, len(got))
	for _, r := range got {
		codes[r.Code] = true
	}
	if !codes["iad"] {
		t.Error("iad should be available via unlockedCodes")
	}
	if codes["pdx"] {
		t.Error("pdx should remain locked for lax user without unlock")
	}
	// Inactive regions must never appear even if explicitly unlocked.
	got2 := AvailableFor("lax", "dev", "ci")
	for _, r := range got2 {
		if r.Code == "dev" || r.Code == "ci" {
			t.Errorf("inactive region %q must not appear even when unlocked", r.Code)
		}
	}
}

func TestAvailableForPrivateGated(t *testing.T) {
	// A lax user without unlocks must not see private regions (dal, iad).
	got := AvailableFor("lax")
	for _, r := range got {
		if r.Private {
			t.Errorf("private region %q leaked to unprivileged lax user", r.Code)
		}
	}
	// A user whose current region is private still sees it.
	got2 := AvailableFor("dal")
	var sawDal bool
	for _, r := range got2 {
		if r.Code == "dal" {
			sawDal = true
		}
	}
	if !sawDal {
		t.Error("dal user should see dal")
	}
	// Unlocking grants access.
	got3 := AvailableFor("lax", "dal")
	var sawDalUnlocked bool
	for _, r := range got3 {
		if r.Code == "dal" {
			sawDalUnlocked = true
		}
	}
	if !sawDalUnlocked {
		t.Error("dal should be available via unlockedCodes")
	}
}

func TestCountryMapNoPrivateRegions(t *testing.T) {
	for cc, r := range countryToRegion {
		if r.Private {
			t.Errorf("countryToRegion[%q] maps to private region %q; automatic routing must not land on private regions", cc, r.Code)
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

func TestAvailableFor(t *testing.T) {
	tests := []struct {
		name        string
		currentCode string
		wantCodes   []string // all must be present
		wantAbsent  []string // none must be present
	}{
		{
			// pdx has RequiresUserMatch, so a pdx user sees pdx plus all open regions.
			name:        "pdx user sees pdx and open regions",
			currentCode: "pdx",
			wantCodes:   []string{"pdx", "lax", "nyc", "fra", "tyo", "syd", "sgp", "lon"},
			wantAbsent:  []string{"iad"},
		},
		{
			// fra is open (!RequiresUserMatch). A fra user sees all open regions but not pdx/iad.
			name:        "fra user sees open regions",
			currentCode: "fra",
			wantCodes:   []string{"lax", "nyc", "fra", "tyo", "syd", "sgp", "lon"},
			wantAbsent:  []string{"pdx", "iad"},
		},
		{
			// Inactive regions (dev, ci) must never appear.
			name:        "inactive regions excluded",
			currentCode: "dev",
			wantAbsent:  []string{"dev", "ci"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AvailableFor(tt.currentCode)
			codes := make(map[string]bool, len(got))
			for _, r := range got {
				codes[r.Code] = true
			}
			for _, want := range tt.wantCodes {
				if !codes[want] {
					t.Errorf("AvailableFor(%q): expected %q to be present, got %v", tt.currentCode, want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if codes[absent] {
					t.Errorf("AvailableFor(%q): expected %q to be absent, got %v", tt.currentCode, absent, got)
				}
			}
			// All returned regions must be active.
			for _, r := range got {
				if !r.Active {
					t.Errorf("AvailableFor(%q): returned inactive region %q", tt.currentCode, r.Code)
				}
			}
		})
	}
}
