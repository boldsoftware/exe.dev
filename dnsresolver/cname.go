// Package dnsresolver provides deterministic DNS lookups for the exe.dev
// control plane. The standard library's net.LookupCNAME may skip over the
// first-hop alias if an authoritative nameserver flattens results, which
// causes us to extract the wrong box name. We bypass that behavior by issuing
// raw DNS queries and returning exactly the published CNAME target so TLS and
// proxy routing stay consistent.
package dnsresolver

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	dnsDefaultTimeout  = 2 * time.Second
	maxDNSPacketLength = 512
)

// overrideDNSForTest can be set by tests to point at a fake resolver.
var overrideDNSForTest string

// LookupCNAME performs a deterministic CNAME lookup for host, returning the
// first-hop alias exactly as published (without recursively following it).
func LookupCNAME(ctx context.Context, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("lookup CNAME: host is empty")
	}
	server := dnsServer()
	name, err := dnsmessage.NewName(ensureTrailingDot(strings.ToLower(host)))
	if err != nil {
		return "", fmt.Errorf("lookup CNAME: invalid host %q: %w", host, err)
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:               dnsMessageID(),
			RecursionDesired: true,
		},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  dnsmessage.TypeCNAME,
			Class: dnsmessage.ClassINET,
		}},
	}
	query, err := msg.Pack()
	if err != nil {
		return "", fmt.Errorf("lookup CNAME: pack query: %w", err)
	}
	answer, err := exchangeCNAME(ctx, server, query, host)
	if err == nil {
		return answer, nil
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return "", err
	}
	return "", err
}

func exchangeCNAME(ctx context.Context, server string, payload []byte, host string) (string, error) {
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return "", fmt.Errorf("dial DNS server %s: %w", server, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(dnsDefaultTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("set DNS deadline: %w", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return "", fmt.Errorf("send DNS query: %w", err)
	}
	buf := make([]byte, maxDNSPacketLength)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read DNS response: %w", err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		return "", fmt.Errorf("unpack DNS response: %w", err)
	}
	switch resp.Header.RCode {
	case dnsmessage.RCodeSuccess:
		for _, answer := range resp.Answers {
			if answer.Header.Type != dnsmessage.TypeCNAME {
				continue
			}
			cname, ok := answer.Body.(*dnsmessage.CNAMEResource)
			if !ok {
				continue
			}
			return normalizeHostname(cname.CNAME.String()), nil
		}
		return normalizeHostname(host), nil
	case dnsmessage.RCodeNameError:
		return "", &net.DNSError{Name: host, Err: "no such host", IsNotFound: true}
	default:
		return "", fmt.Errorf("dns error %v", resp.Header.RCode)
	}
}

func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func ensureTrailingDot(host string) string {
	if host == "" {
		return "."
	}
	if strings.HasSuffix(host, ".") {
		return host
	}
	return host + "."
}

func dnsMessageID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint16(b[:])
}

func dnsServer() string {
	if overrideDNSForTest != "" {
		return overrideDNSForTest
	}
	if runtime.GOOS == "linux" {
		return "127.0.0.53:53"
	}
	return "8.8.8.8:53"
}
