package execore

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"

	"exe.dev/cobble"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func TestServerGetCertificate(t *testing.T) {
	s := newUnstartedServer(t)
	s.lookupCNAMEFunc = func(ctx context.Context, host string) (string, error) {
		switch host {
		case "example.com":
			return "example.exe.local", nil
		default:
			return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.certManager = &autocert.Manager{
		Cache:      autocert.DirCache(t.TempDir()),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: s.hostPolicy,
		Client: func() *acme.Client {
			stone, err := cobble.Start(context.Background(), &cobble.Config{
				AlwaysValid: true,
				Log:         t.Output(),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { stone.Stop() })
			return stone.Client()
		}(),
	}

	// Start http only (which is all we need to test GetCertificate using ACME)
	s.startAndAwaitReady()

	s.createTestBox(t, "uid", "aid", "example", "cid", "exeuntu")

	_, err := s.getCertificate(&tls.ClientHelloInfo{ServerName: "example.com"})
	if err != nil {
		t.Fatalf("expected certificate for example.com, got: %v", err)
	}

	_, err = s.getCertificate(&tls.ClientHelloInfo{ServerName: "nonexistent.com"})
	var got *net.DNSError
	if !errors.As(err, &got) {
		t.Fatalf("expected DNSError for nonexistent.com, got: %v", err)
	}
	if got.Name != "nonexistent.com" || !got.IsNotFound {
		t.Fatalf("expected DNSError for nonexistent.com, got: %v", err)
	}
}
