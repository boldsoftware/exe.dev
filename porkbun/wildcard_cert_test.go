package porkbun

import (
	"testing"
)

func TestWildcardCertManager_getCertificateKey(t *testing.T) {
	w := &WildcardCertManager{
		domain: "exe.dev",
	}

	tests := []struct {
		name       string
		serverName string
		want       string
	}{
		{
			name:       "main domain",
			serverName: "exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "www subdomain",
			serverName: "www.exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "single level subdomain",
			serverName: "api.exe.dev",
			want:       "*.exe.dev",
		},
		{
			name:       "machine.team two level subdomain",
			serverName: "myapp.myteam.exe.dev",
			want:       "*.myteam.exe.dev",
		},
		{
			name:       "another machine.team subdomain",
			serverName: "webapp.devteam.exe.dev",
			want:       "*.devteam.exe.dev",
		},
		{
			name:       "three level subdomain - not supported",
			serverName: "baz.foo.bar.exe.dev",
			want:       "baz.foo.bar.exe.dev", // Returns unchanged - will fail cert acquisition
		},
		{
			name:       "unrelated domain",
			serverName: "example.com",
			want:       "example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := w.getCertificateKey(tt.serverName)
			if got != tt.want {
				t.Errorf("getCertificateKey(%q) = %q, want %q", tt.serverName, got, tt.want)
			}
		})
	}
}
