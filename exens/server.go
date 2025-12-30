// Package exens provides an embedded DNS nameserver for exed.
// It serves DNS records from the SQLite database, enabling immediate
// DNS updates without waiting for Route53 propagation.
//
// Record sources:
//   - A records (sNNN.exe.xyz): ip_shards table
//   - CNAME records (vmname.exe.xyz): boxes + box_ip_shard tables
//   - TXT records (ACME challenges): in-memory map
package exens

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"exe.dev/exedb"
	"exe.dev/publicips"
	"exe.dev/sqlite"
)

// Server is an embedded DNS nameserver that serves records from the database.
type Server struct {
	db  *sqlite.DB
	log *slog.Logger

	mu        sync.Mutex
	listeners []net.PacketConn
	servers   []*dns.Server

	// In-memory TXT records for ACME challenges.
	// Key: FQDN (e.g., "_acme-challenge.exe.xyz")
	// Value: list of TXT values
	txtMu      sync.RWMutex
	txtRecords map[string][]string
}

// NewServer creates a new DNS server backed by the given database.
func NewServer(db *sqlite.DB, log *slog.Logger) *Server {
	return &Server{
		db:         db,
		log:        log,
		txtRecords: make(map[string][]string),
	}
}

// Start starts the DNS server listening on the given private IP addresses.
// Each IP will have a DNS server listening on UDP port 53.
func (s *Server) Start(ctx context.Context, privateIPs []netip.Addr) error {
	if len(privateIPs) == 0 {
		return fmt.Errorf("exens: no IPs to listen on")
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleDNS)

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ip := range privateIPs {
		addr := net.JoinHostPort(ip.String(), "53")
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			// Clean up any already-started listeners
			for _, c := range s.listeners {
				c.Close()
			}
			s.listeners = nil
			return fmt.Errorf("exens: failed to listen on %s: %w", addr, err)
		}

		server := &dns.Server{
			PacketConn: conn,
			Net:        "udp",
			Handler:    mux,
		}

		s.listeners = append(s.listeners, conn)
		s.servers = append(s.servers, server)

		go func(ctx context.Context, addr string) {
			s.log.InfoContext(ctx, "DNS server starting", "addr", addr)
			if err := server.ActivateAndServe(); err != nil && !strings.Contains(err.Error(), "bad listeners") {
				s.log.ErrorContext(ctx, "DNS server error", "addr", addr, "error", err)
			}
		}(ctx, addr)
	}

	return nil
}

// Stop gracefully stops all DNS servers.
func (s *Server) Stop(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, server := range s.servers {
		server.Shutdown(ctx)
	}
	s.servers = nil
	s.listeners = nil
}

// Handler returns a DNS handler function for use with dns.ServeMux.
func (s *Server) Handler() func(context.Context, dns.ResponseWriter, *dns.Msg) {
	return s.handleDNS
}

// SetTXTRecord sets an in-memory TXT record (used for ACME challenges).
func (s *Server) SetTXTRecord(name, value string) {
	name = strings.ToLower(name)
	s.txtMu.Lock()
	defer s.txtMu.Unlock()
	s.txtRecords[name] = append(s.txtRecords[name], value)
}

// DeleteTXTRecord removes an in-memory TXT record value.
func (s *Server) DeleteTXTRecord(name, value string) {
	name = strings.ToLower(name)
	s.txtMu.Lock()
	defer s.txtMu.Unlock()
	values := s.txtRecords[name]
	for i, v := range values {
		if v == value {
			s.txtRecords[name] = append(values[:i], values[i+1:]...)
			break
		}
	}
	if len(s.txtRecords[name]) == 0 {
		delete(s.txtRecords, name)
	}
}

// GetTXTRecords returns TXT record values for a name (for testing).
func (s *Server) GetTXTRecords(name string) []string {
	name = strings.ToLower(name)
	s.txtMu.RLock()
	defer s.txtMu.RUnlock()
	return append([]string{}, s.txtRecords[name]...)
}

// handleDNS processes DNS queries.
func (s *Server) handleDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) {
	resp := new(dns.Msg)
	resp.Authoritative = true
	dnsutil.SetReply(resp, r)

	var foundAny bool
	for _, question := range r.Question {
		header := question.Header()
		qname := strings.ToLower(strings.TrimSuffix(header.Name, "."))
		qtype := dns.RRToType(question)

		var rrs []dns.RR
		var err error

		switch qtype {
		case dns.TypeA:
			rrs, err = s.lookupA(ctx, qname, header.Name, header.Class)
		case dns.TypeCNAME:
			rrs, err = s.lookupCNAME(ctx, qname, header.Name, header.Class)
		case dns.TypeTXT:
			rrs, err = s.lookupTXT(ctx, qname, header.Name, header.Class)
		default:
			s.log.DebugContext(ctx, "unsupported query type", "name", qname, "type", dns.TypeToString[qtype])
		}

		if err != nil {
			s.log.WarnContext(ctx, "DNS lookup error", "name", qname, "type", dns.TypeToString[qtype], "error", err)
			resp.Rcode = dns.RcodeServerFailure
			if _, err := resp.WriteTo(w); err != nil {
				s.log.WarnContext(ctx, "DNS response write error", "error", err)
			}
			return
		}

		if len(rrs) > 0 {
			resp.Answer = append(resp.Answer, rrs...)
			foundAny = true
		}
	}

	if !foundAny {
		resp.Rcode = dns.RcodeNameError
	}

	if _, err := resp.WriteTo(w); err != nil {
		s.log.WarnContext(ctx, "DNS response write error", "error", err)
	}
}

// lookupA handles A record queries.
// Format: sNNN.{domain} where NNN is a shard number (001-025)
// For box names, returns the CNAME and chases it to get the A record.
func (s *Server) lookupA(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Parse shard from name (e.g., "s001.exe.xyz" -> shard 1)
	shard, err := parseShardFromName(qname)
	if err != nil {
		// Not a shard name, check if there's a CNAME (box name)
		cnameRRs, err := s.lookupCNAME(ctx, qname, fqdn, class)
		if err != nil || len(cnameRRs) == 0 {
			return nil, err
		}
		// Got a CNAME, now chase it to get the A record
		cname := cnameRRs[0].(*dns.CNAME)
		targetName := strings.TrimSuffix(cname.Target, ".")
		aRRs, err := s.lookupA(ctx, targetName, cname.Target, class)
		if err != nil {
			return cnameRRs, nil // Return just the CNAME if we can't resolve it
		}
		// Return CNAME followed by the A record
		return append(cnameRRs, aRRs...), nil
	}

	var publicIP string
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		var err error
		publicIP, err = queries.GetShardPublicIP(ctx, int64(shard))
		return err
	})
	if err != nil {
		// No record found is not an error, just return empty
		return nil, nil
	}

	ip := net.ParseIP(publicIP)
	if ip == nil {
		s.log.WarnContext(ctx, "invalid IP in ip_shards table", "shard", shard, "ip", publicIP)
		return nil, nil
	}

	return []dns.RR{
		&dns.A{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 300},
			A:   ip.To4(),
		},
	}, nil
}

// lookupCNAME handles CNAME record queries.
// Format: {boxname}.{domain} -> sNNN.{domain}
func (s *Server) lookupCNAME(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Extract box name (everything before first dot)
	parts := strings.SplitN(qname, ".", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	boxName := parts[0]
	domain := parts[1]

	// Skip shard names (s001, s002, etc.)
	if _, err := parseShardFromName(qname); err == nil {
		return nil, nil
	}

	var shard int64
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		var err error
		shard, err = queries.GetIPShardByBoxName(ctx, boxName)
		return err
	})
	if err != nil {
		// No record found
		return nil, nil
	}

	target := publicips.ShardSub(int(shard)) + "." + domain + "."

	return []dns.RR{
		&dns.CNAME{
			Hdr:    dns.Header{Name: fqdn, Class: class, TTL: 300},
			Target: target,
		},
	}, nil
}

// lookupTXT handles TXT record queries from in-memory storage.
func (s *Server) lookupTXT(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	s.txtMu.RLock()
	values := s.txtRecords[qname]
	s.txtMu.RUnlock()

	if len(values) == 0 {
		return nil, nil
	}

	var rrs []dns.RR
	for _, v := range values {
		rrs = append(rrs, &dns.TXT{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 60},
			Txt: []string{v},
		})
	}
	return rrs, nil
}

// parseShardFromName extracts the shard number from a DNS name.
// Returns error if the name is not a shard name (e.g., "s001.exe.xyz" -> 1).
func parseShardFromName(name string) (int, error) {
	parts := strings.SplitN(name, ".", 2)
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid name")
	}
	sub := parts[0]
	if len(sub) != 4 || sub[0] != 's' {
		return 0, fmt.Errorf("not a shard name")
	}
	shard, err := strconv.Atoi(sub[1:])
	if err != nil || shard < 1 || shard > publicips.MaxDomainShards {
		return 0, fmt.Errorf("invalid shard number")
	}
	return shard, nil
}
