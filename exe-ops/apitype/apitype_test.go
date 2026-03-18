package apitype

import "testing"

func TestParseHostname(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    HostnameParts
		wantErr bool
	}{
		{
			name:  "valid exelet hostname",
			input: "exelet-nyc-prod-01",
			want:  HostnameParts{Role: "exelet", Region: "nyc", Env: "prod", Instance: "01"},
		},
		{
			name:  "valid exeprox hostname",
			input: "exeprox-lax-staging-02",
			want:  HostnameParts{Role: "exeprox", Region: "lax", Env: "staging", Instance: "02"},
		},
		{
			name:  "instance with dashes",
			input: "exelet-nyc-prod-web-01",
			want:  HostnameParts{Role: "exelet", Region: "nyc", Env: "prod", Instance: "web-01"},
		},
		{
			name:  "legacy exe-ctr host",
			input: "exe-ctr-04",
			want:  HostnameParts{Role: "exelet", Region: "pdx", Env: "prod", Instance: "04"},
		},
		{
			name:  "legacy exe-ctr host with multi-part instance",
			input: "exe-ctr-web-01",
			want:  HostnameParts{Role: "exelet", Region: "pdx", Env: "prod", Instance: "web-01"},
		},
		{
			name:    "legacy exe-ctr with no instance",
			input:   "exe-ctr-",
			wantErr: true,
		},
		{
			name:    "too few parts",
			input:   "exelet-nyc",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "empty component",
			input:   "exelet--prod-01",
			wantErr: true,
		},
		{
			name:  "replica server",
			input: "exelet-nyc-prod-01-replica",
			want:  HostnameParts{Role: "replica", Region: "nyc", Env: "prod", Instance: "01"},
		},
		{
			name:  "replica server different region",
			input: "exelet-lax-prod-03-replica",
			want:  HostnameParts{Role: "replica", Region: "lax", Env: "prod", Instance: "03"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHostname(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseHostname(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseHostname(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}
