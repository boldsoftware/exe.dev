package execore

import "testing"

func TestClassifyDNSResult(t *testing.T) {
	boxHost := "exe.xyz"

	tests := []struct {
		name       string
		result     dnsCheckResult
		wantStatus string
	}{
		{
			name: "cname directly to exe.xyz",
			result: dnsCheckResult{
				Domain:           "app.example.com",
				CNAME:            "myvm.exe.xyz",
				CNAMEPointsToExe: true,
				BoxName:          "myvm",
			},
			wantStatus: "ok",
		},
		{
			name: "cname to wrong target",
			result: dnsCheckResult{
				Domain: "app.example.com",
				CNAME:  "other.example.net",
			},
			wantStatus: "error",
		},
		{
			name: "apex with A pointing to exe and www set",
			result: dnsCheckResult{
				Domain:      "example.com",
				IsApex:      true,
				ARecords:    []string{"1.2.3.4"},
				PointsToExe: true,
				WWWCNAME:    "myvm.exe.xyz",
				BoxName:     "myvm",
			},
			wantStatus: "ok",
		},
		{
			name: "apex with A pointing to exe but www missing",
			result: dnsCheckResult{
				Domain:      "example.com",
				IsApex:      true,
				ARecords:    []string{"1.2.3.4"},
				PointsToExe: true,
				WWWMissing:  true,
			},
			wantStatus: "partial",
		},
		{
			name: "apex with A not pointing to exe",
			result: dnsCheckResult{
				Domain:   "example.com",
				IsApex:   true,
				ARecords: []string{"9.9.9.9"},
			},
			wantStatus: "error",
		},
		{
			name: "apex with CNAME (RFC 1912 violation)",
			result: dnsCheckResult{
				Domain:           "example.com",
				CNAME:            "myvm.exe.xyz",
				CNAMEPointsToExe: true,
				BoxName:          "myvm",
				ApexCNAME:        true,
			},
			wantStatus: "error",
		},
		{
			name: "no records at all",
			result: dnsCheckResult{
				Domain:     "nonexistent.example.com",
				CNAMEError: "no CNAME record found",
				AError:     "no A records found",
			},
			wantStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := classifyDNSResult(&tt.result, boxHost)
			if status != tt.wantStatus {
				t.Errorf("classifyDNSResult() status = %q, want %q (message: %s)", status, tt.wantStatus, msg)
			}
			if msg == "" {
				t.Error("classifyDNSResult() returned empty message")
			}
		})
	}
}

func TestIsApexDomain(t *testing.T) {
	tests := []struct {
		domain string
		want   bool
	}{
		{"example.com", true},
		{"app.example.com", false},
		{"example.co.uk", true},
		{"foo.example.co.uk", false},
		{"com", false},
	}
	for _, tt := range tests {
		if got := isApexDomain(tt.domain); got != tt.want {
			t.Errorf("isApexDomain(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
}
