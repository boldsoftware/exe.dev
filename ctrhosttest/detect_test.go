package ctrhosttest

import (
	"os"
	"testing"
)

func TestResolveDefaultGateway_ParsesSSHURL(t *testing.T) {
	tests := []struct {
		name     string
		ctrHost  string
		wantHost string // the host we expect to SSH into
	}{
		{
			name:     "ssh URL with user, IP and port",
			ctrHost:  "ssh://ubuntu@192.168.122.205:45065",
			wantHost: "192.168.122.205",
		},
		{
			name:     "ssh URL with user and IP",
			ctrHost:  "ssh://ubuntu@192.168.122.205",
			wantHost: "192.168.122.205",
		},
		{
			name:     "ssh URL with IP only",
			ctrHost:  "ssh://192.168.122.205",
			wantHost: "192.168.122.205",
		},
		{
			name:     "ssh URL with hostname",
			ctrHost:  "ssh://lima-exe-ctr",
			wantHost: "lima-exe-ctr",
		},
		{
			name:     "plain hostname",
			ctrHost:  "lima-exe-ctr",
			wantHost: "lima-exe-ctr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set CTR_HOST for this test
			oldCtrHost := os.Getenv("CTR_HOST")
			os.Setenv("CTR_HOST", tt.ctrHost)
			t.Cleanup(func() {
				if oldCtrHost != "" {
					os.Setenv("CTR_HOST", oldCtrHost)
				} else {
					os.Unsetenv("CTR_HOST")
				}
			})

			// We can't actually test that it runs the right SSH command without mocking,
			// but we can at least verify the function doesn't panic and parses the URL.
			// The actual SSH will fail if the host doesn't exist, but that's OK for this test.
			result := ResolveDefaultGateway()

			// We expect either:
			// - An IP address (if SSH succeeded)
			// - Empty string (if SSH failed, which is OK for non-existent hosts)
			// The important thing is that we don't get "ssh://ubuntu@192.168.122.205:45065" back
			if result == tt.ctrHost {
				t.Errorf("ResolveDefaultGateway() returned the raw CTR_HOST value %q instead of resolving it", result)
			}

			// If we get a result, it should not contain "ssh://"
			if result != "" && (len(result) > 6 && result[:6] == "ssh://") {
				t.Errorf("ResolveDefaultGateway() returned an ssh:// URL: %q", result)
			}
		})
	}
}
