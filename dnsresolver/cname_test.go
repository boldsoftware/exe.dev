package dnsresolver

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestLookupCNAMEReturnsFirstHop(t *testing.T) {
	addr, cleanup := startTestDNSServer(t, func(req dnsmessage.Message) dnsmessage.Message {
		name := req.Questions[0].Name
		return dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 req.Header.ID,
				Response:           true,
				Authoritative:      true,
				RecursionAvailable: true,
			},
			Questions: req.Questions,
			Answers: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{
						Name:  name,
						Type:  dnsmessage.TypeCNAME,
						Class: dnsmessage.ClassINET,
						TTL:   60,
					},
					Body: &dnsmessage.CNAMEResource{CNAME: mustNewName("knownhosts.exe.xyz.")},
				},
			},
		}
	})
	t.Cleanup(cleanup)
	overrideDNSForTest = addr
	t.Cleanup(func() { overrideDNSForTest = "" })
	cname, err := LookupCNAME(context.Background(), "www.knownhosts.net")
	if err != nil {
		t.Fatalf("LookupCNAME error: %v", err)
	}
	if cname != "knownhosts.exe.xyz" {
		t.Fatalf("LookupCNAME returned %q, want %q", cname, "knownhosts.exe.xyz")
	}
}

func TestLookupCNAMEPrefersFirstHopWhenMultipleAnswers(t *testing.T) {
	addr, cleanup := startTestDNSServer(t, func(req dnsmessage.Message) dnsmessage.Message {
		name := req.Questions[0].Name
		return dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 req.Header.ID,
				Response:           true,
				Authoritative:      true,
				RecursionAvailable: true,
			},
			Questions: req.Questions,
			Answers: []dnsmessage.Resource{
				{
					Header: dnsmessage.ResourceHeader{
						Name:  name,
						Type:  dnsmessage.TypeCNAME,
						Class: dnsmessage.ClassINET,
						TTL:   600,
					},
					Body: &dnsmessage.CNAMEResource{CNAME: mustNewName("knownhosts.exe.xyz.")},
				},
				{
					Header: dnsmessage.ResourceHeader{
						Name:  mustNewName("knownhosts.exe.xyz."),
						Type:  dnsmessage.TypeCNAME,
						Class: dnsmessage.ClassINET,
						TTL:   300,
					},
					Body: &dnsmessage.CNAMEResource{CNAME: mustNewName("s001.exe.xyz.")},
				},
				{
					Header: dnsmessage.ResourceHeader{
						Name:  mustNewName("s001.exe.xyz."),
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   300,
					},
					Body: &dnsmessage.AResource{A: [4]byte{52, 35, 87, 134}},
				},
			},
		}
	})
	t.Cleanup(cleanup)
	overrideDNSForTest = addr
	t.Cleanup(func() { overrideDNSForTest = "" })
	cname, err := LookupCNAME(context.Background(), "www.knownhosts.net")
	if err != nil {
		t.Fatalf("LookupCNAME error: %v", err)
	}
	if cname != "knownhosts.exe.xyz" {
		t.Fatalf("LookupCNAME returned %q, want %q", cname, "knownhosts.exe.xyz")
	}
}

func TestLookupCNAMEFallsBackToHost(t *testing.T) {
	addr, cleanup := startTestDNSServer(t, func(req dnsmessage.Message) dnsmessage.Message {
		return dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 req.Header.ID,
				Response:           true,
				Authoritative:      true,
				RecursionAvailable: true,
			},
			Questions: req.Questions,
		}
	})
	t.Cleanup(cleanup)
	overrideDNSForTest = addr
	t.Cleanup(func() { overrideDNSForTest = "" })
	const name = "example.com"
	cname, err := LookupCNAME(context.Background(), name)
	if err != nil {
		t.Fatalf("LookupCNAME error: %v", err)
	}
	if cname != name {
		t.Fatalf("LookupCNAME returned %q, want %q", cname, name)
	}
}

func TestLookupCNAMENXDOMAIN(t *testing.T) {
	addr, cleanup := startTestDNSServer(t, func(req dnsmessage.Message) dnsmessage.Message {
		return dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 req.Header.ID,
				Response:           true,
				Authoritative:      true,
				RecursionAvailable: true,
				RCode:              dnsmessage.RCodeNameError,
			},
			Questions: req.Questions,
		}
	})
	t.Cleanup(cleanup)
	overrideDNSForTest = addr
	t.Cleanup(func() { overrideDNSForTest = "" })
	_, err := LookupCNAME(context.Background(), "missing.example")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatalf("expected net.DNSError IsNotFound, got %v", err)
	}
}

func startTestDNSServer(t *testing.T, handler func(req dnsmessage.Message) dnsmessage.Message) (string, func()) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, maxDNSPacketLength)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			resp := handler(msg)
			resp.Header.ID = msg.Header.ID
			resp.Questions = msg.Questions
			b, err := resp.Pack()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(b, addr)
		}
	}()
	cleanup := func() {
		_ = pc.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	return pc.LocalAddr().String(), cleanup
}

func mustNewName(name string) dnsmessage.Name {
	res, err := dnsmessage.NewName(name)
	if err != nil {
		panic(err)
	}
	return res
}
