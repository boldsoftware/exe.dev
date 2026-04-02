package exeweb

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"

	"exe.dev/publicips"
	"exe.dev/stage"
)

func TestParentDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host string
		want string
	}{
		{"x.y.z", "y.z"},
		{"a.b.c.d", "b.c.d"},
		{"y.z", ""}, // only 2 labels, parent has 1 label
		{"z", ""},   // single label
		{"", ""},    // empty
	}
	for _, tt := range tests {
		got := parentDomain(tt.host)
		if got != tt.want {
			t.Errorf("parentDomain(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

func TestCheckWildcardCNAME(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		host  string
		cname string
		// CNAME responses: host -> target
		cnames     map[string]string
		wantLogged bool
	}{
		{
			name:  "wildcard detected",
			host:  "random.attacker.com",
			cname: "mybox.exe.xyz",
			cnames: map[string]string{
				"attacker.com": "mybox.exe.xyz",
			},
			wantLogged: true,
		},
		{
			name:  "parent has different CNAME",
			host:  "sub.legit.com",
			cname: "mybox.exe.xyz",
			cnames: map[string]string{
				"legit.com": "otherbox.exe.xyz",
			},
			wantLogged: false,
		},
		{
			name:       "parent has no CNAME",
			host:       "sub.legit.com",
			cname:      "mybox.exe.xyz",
			cnames:     map[string]string{},
			wantLogged: false,
		},
		{
			name:       "two-label host has no parent to check",
			host:       "legit.com",
			cname:      "mybox.exe.xyz",
			cnames:     map[string]string{},
			wantLogged: false,
		},
		{
			name:  "deep wildcard detected",
			host:  "a.b.attacker.com",
			cname: "mybox.exe.xyz",
			cnames: map[string]string{
				"b.attacker.com": "mybox.exe.xyz",
			},
			wantLogged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			dr := &DomainResolver{
				Lg:  lg,
				Env: ptrTo(stage.Prod()),
				LookupCNAMEFunc: func(_ context.Context, host string) (string, error) {
					if target, ok := tt.cnames[host]; ok {
						return target, nil
					}
					return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
				},
			}

			dr.checkWildcardCNAME(context.Background(), tt.host, tt.cname)

			logged := buf.String()
			hasWarning := strings.Contains(logged, "probable wildcard CNAME detected")
			if hasWarning != tt.wantLogged {
				if tt.wantLogged {
					t.Errorf("expected wildcard CNAME warning, got log output: %q", logged)
				} else {
					t.Errorf("unexpected wildcard CNAME warning in log output: %q", logged)
				}
			}
		})
	}
}

func TestResolveCustomDomainBoxNameLogsWildcardCNAME(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	dr := &DomainResolver{
		Lg:  lg,
		Env: ptrTo(stage.Prod()),
		PublicIPs: map[netip.Addr]publicips.PublicIP{
			netip.MustParseAddr("10.0.0.5"): {
				IP:     netip.MustParseAddr("203.0.113.10"),
				Domain: "na001.exe.xyz",
			},
		},
		LookupCNAMEFunc: func(_ context.Context, host string) (string, error) {
			switch host {
			case "random.attacker.com":
				return "mybox.exe.xyz", nil
			case "attacker.com":
				// Same target — this is a wildcard CNAME (*.attacker.com)
				return "mybox.exe.xyz", nil
			default:
				return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
		},
	}

	boxName, err := dr.ResolveCustomDomainBoxName(context.Background(), "random.attacker.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if boxName != "mybox" {
		t.Fatalf("got boxName=%q, want %q", boxName, "mybox")
	}

	logged := buf.String()
	if !strings.Contains(logged, "probable wildcard CNAME detected") {
		t.Errorf("expected wildcard CNAME warning in log, got: %q", logged)
	}
	if !strings.Contains(logged, "attacker.com") {
		t.Errorf("expected parent domain in log, got: %q", logged)
	}
}

func ptrTo[T any](v T) *T {
	return &v
}
