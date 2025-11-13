package route53

import "testing"

func TestIsSingleLevelSubdomain(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name       string
		domain     string
		serverName string
		want       bool
	}{
		{
			name:       "apex is not subdomain",
			domain:     "exe.dev",
			serverName: "exe.dev",
			want:       false,
		},
		{
			name:       "single level subdomain",
			domain:     "exe.dev",
			serverName: "api.exe.dev",
			want:       true,
		},
		{
			name:       "multi level subdomain rejected",
			domain:     "exe.dev",
			serverName: "box.team.exe.dev",
			want:       false,
		},
		{
			name:       "www single level subdomain",
			domain:     "exe.dev",
			serverName: "www.exe.dev",
			want:       true,
		},
		{
			name:       "subdomain of nested root",
			domain:     "xterm.exe.dev",
			serverName: "console.xterm.exe.dev",
			want:       true,
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isSingleLevelSubdomain(tc.domain, tc.serverName)
			if got != tc.want {
				t.Fatalf("isSingleLevelSubdomain(%q, %q) = %v, want %v", tc.domain, tc.serverName, got, tc.want)
			}
		})
	}
}

func TestWildcardCertManager_domainForServerName(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name       string
		domains    []string
		serverName string
		want       string
	}{
		{
			name:       "apex domain match",
			domains:    []string{"exe.dev"},
			serverName: "exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "single level subdomain collapses to root",
			domains:    []string{"exe.dev"},
			serverName: "api.exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "multi level subdomain rejected",
			domains:    []string{"exe.dev"},
			serverName: "machine.team.exe.dev",
			want:       "",
		},
		{
			name:       "prefer exact match over parent root",
			domains:    []string{"exe.dev", "xterm.exe.dev"},
			serverName: "xterm.exe.dev",
			want:       "xterm.exe.dev",
		},
		{
			name:       "single level subdomain of nested root",
			domains:    []string{"exe.dev", "xterm.exe.dev"},
			serverName: "console.xterm.exe.dev",
			want:       "xterm.exe.dev",
		},
		{
			name:       "unknown domain",
			domains:    []string{"exe.dev"},
			serverName: "example.com",
			want:       "",
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &WildcardCertManager{
				domains: tc.domains,
			}
			got := w.domainForServerName(tc.serverName)
			if got != tc.want {
				t.Fatalf("domainForServerName(%q) = %q, want %q", tc.serverName, got, tc.want)
			}
		})
	}
}
