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
	"crypto/sha256"
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

	// boxHost is the domain for boxes (e.g., "exe.xyz" or "exe-staging.xyz")
	boxHost string
	// webHost is the domain for the name servers (e.g., "exe.dev" or "exe-staging.dev")
	webHost string

	// lobbyIP is the public IP for the lobby/REPL (ssh exe.dev), used for apex domain resolution.
	// Set via SetLobbyIP after discovering IPs at startup.
	lobbyIP netip.Addr

	mu        sync.Mutex
	listeners []net.PacketConn
	servers   []*dns.Server

	// In-memory TXT records for ACME challenges.
	// Key: FQDN (e.g., "_acme-challenge.exe.xyz")
	// Value: list of TXT values
	txtMu      sync.RWMutex
	txtRecords map[string][]string

	// GLB rollout prefixes for hash-prefix gating.
	// Each prefix is a binary string (e.g., "0101").
	// A user matches if the binary representation of SHA-256(userID) starts with any prefix.
	glbPrefixMu sync.RWMutex
	glbPrefixes []string
}

// NewServer creates a new DNS server backed by the given database.
// boxHost is the domain for boxes (e.g., "exe.xyz" or "exe-staging.xyz").
// webHost is the domain for the name servers (e.g., "exe.dev" or "exe-staging.dev").
func NewServer(db *sqlite.DB, log *slog.Logger, boxHost, webHost string) *Server {
	return &Server{
		db:         db,
		log:        log,
		boxHost:    boxHost,
		webHost:    webHost,
		txtRecords: make(map[string][]string),
	}
}

// SetLobbyIP sets the lobby IP used for apex domain (exe.xyz) resolution.
// The lobby IP is the public IP for ssh exe.dev, not associated with any box shard.
func (s *Server) SetLobbyIP(ip netip.Addr) {
	s.lobbyIP = ip
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

// SetGLBRolloutPrefixes sets the binary prefixes used for hash-prefix gating
// of the GLB rollout. Each prefix is a binary string (e.g., "0101").
func (s *Server) SetGLBRolloutPrefixes(prefixes []string) {
	s.glbPrefixMu.Lock()
	defer s.glbPrefixMu.Unlock()
	s.glbPrefixes = prefixes
}

// GLBRolloutPrefixes returns a copy of the current GLB rollout prefixes.
func (s *Server) GLBRolloutPrefixes() []string {
	s.glbPrefixMu.RLock()
	defer s.glbPrefixMu.RUnlock()
	return append([]string{}, s.glbPrefixes...)
}

// userMatchesGLBPrefix returns true if the user ID matches any of the
// configured GLB rollout prefixes. It hashes the user ID with SHA-256,
// converts the hash to a binary string, and checks whether any prefix
// is a prefix of that string.
func (s *Server) userMatchesGLBPrefix(userID string) bool {
	s.glbPrefixMu.RLock()
	prefixes := s.glbPrefixes
	s.glbPrefixMu.RUnlock()

	if len(prefixes) == 0 {
		return false
	}

	hash := sha256.Sum256([]byte(userID))
	var bin strings.Builder
	for _, b := range hash {
		fmt.Fprintf(&bin, "%08b", b)
	}
	binStr := bin.String()

	for _, prefix := range prefixes {
		if strings.HasPrefix(binStr, prefix) {
			return true
		}
	}
	return false
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
		case dns.TypeMX:
			rrs, err = s.lookupMX(ctx, qname, header.Name, header.Class)
		case dns.TypeNS:
			rrs, err = s.lookupNS(ctx, qname, header.Name, header.Class)
		case dns.TypeSOA:
			rrs, err = s.lookupSOA(ctx, qname, header.Name, header.Class)
		default:
			// This is a very common log line if enabled.
			// s.log.DebugContext(ctx, "unsupported query type", "name", qname, "type", dns.TypeToString[qtype])
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
		// Distinguish NXDOMAIN (name doesn't exist) from NODATA (name exists
		// but no records of the requested type). Rather than maintaining a
		// separate name-existence check (which could drift out of sync with
		// the lookup functions), we call every lookup function: if any would
		// return records, the name exists.
		//
		// This is mildly inefficient, but saves implementing the lookup logic
		// twice to compute a "does this exist" bit. If you want to optimize
		// this, do in-memory result caching from the DB in the lookup functions.
		lookups := []func(context.Context, string, string, uint16) ([]dns.RR, error){
			s.lookupA, s.lookupCNAME, s.lookupTXT, s.lookupMX, s.lookupNS, s.lookupSOA,
		}
		nameExists := false
		for _, question := range r.Question {
			qname := strings.ToLower(strings.TrimSuffix(question.Header().Name, "."))
			fqdn := question.Header().Name
			class := question.Header().Class
			for _, lookup := range lookups {
				rrs, err := lookup(ctx, qname, fqdn, class)
				if err != nil {
					s.log.WarnContext(ctx, "DNS lookup error", "name", qname, "type", dns.TypeToString[dns.RRToType(question)], "error", err)
					resp.Rcode = dns.RcodeServerFailure
					if _, err := resp.WriteTo(w); err != nil {
						s.log.WarnContext(ctx, "DNS response write error", "error", err)
					}
					return
				}
				if len(rrs) > 0 {
					nameExists = true
					break
				}
			}
			if nameExists {
				break
			}
		}
		if !nameExists {
			resp.Rcode = dns.RcodeNameError
		}
	}

	if _, err := resp.WriteTo(w); err != nil {
		s.log.WarnContext(ctx, "DNS response write error", "error", err)
	}
}

// lookupA handles A record queries.
// Format: sNNN.{domain} where NNN is a shard number (001-025)
// For box names, returns the CNAME and chases it to get the A record.
// For *.xterm.{boxHost} and *.shelley.{boxHost}, returns the box CNAME and A.
// For *.int.{boxHost}, returns the metadata IP (169.254.169.254).
// For the base domain ({boxHost}), returns the lobby IP.
// For mail.{boxHost}, returns the lobby IP (mail server).
func (s *Server) lookupA(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Check for base domain (exe.xyz) - return lobby IP
	if qname == s.boxHost {
		return s.lookupLobbyA(fqdn, class)
	}

	// Check for mail subdomain (mail.exe.xyz) - return lobby IP for mail server
	if qname == "mail."+s.boxHost {
		return s.lookupLobbyA(fqdn, class)
	}

	// Check for xterm/shelley wildcard (*.xterm.{boxHost} or *.shelley.{boxHost})
	// e.g., "anything.xterm.exe.xyz" or "foo.shelley.exe.xyz"
	box, ok := strings.CutSuffix(qname, ".xterm."+s.boxHost)
	if !ok {
		box, ok = strings.CutSuffix(qname, ".shelley."+s.boxHost)
	}
	if ok && len(box) > 0 {
		// Handle xterm/shelley as a box name.
		// If that fails send them to the lobby,
		// as that is what we've historically done.
		// TODO: stop sending unknown names to lobby?
		rrs, err := s.lookupBoxA(ctx, box+"."+s.boxHost, fqdn, class)
		if err != nil || len(rrs) == 0 {
			return s.lookupLobbyA(fqdn, class)
		}
		return rrs, err
	}

	// Check for integration wildcard (*.int.{boxHost})
	// e.g., "myproxy.int.exe.xyz" → 169.254.169.254 (metadata service)
	intSuffix := ".int." + s.boxHost
	if strings.HasSuffix(qname, intSuffix) {
		return s.lookupMetadataA(fqdn, class)
	}

	// Parse shard from name (e.g., "s001.exe.xyz" -> shard 1)
	shard, err := parseShardFromName(qname)
	if err == nil {
		return s.lookupShardA(ctx, shard, fqdn, class)
	}

	// Parse latitude shard from name (e.g., "n043.exe.xyz" -> shard 43)
	latShard, err := parseLatitudeShardFromName(qname)
	if err == nil {
		return s.lookupLatitudeShardA(ctx, latShard, fqdn, class)
	}

	// Not a shard name, check if there's a CNAME (box name)
	return s.lookupBoxA(ctx, qname, fqdn, class)
}

// lookupBoxA looks for a CNAME for a box name,
// and returns the CNAME and A record.
func (s *Server) lookupBoxA(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
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

// lookupShardA returns an A record for the given shard number.
func (s *Server) lookupShardA(ctx context.Context, shard int, fqdn string, class uint16) ([]dns.RR, error) {
	publicIP, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetShardPublicIP, int64(shard))
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

// lookupLatitudeShardA returns an A record for the given latitude shard number.
func (s *Server) lookupLatitudeShardA(ctx context.Context, shard int, fqdn string, class uint16) ([]dns.RR, error) {
	publicIP, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetLatitudeShardPublicIP, int64(shard))
	if err != nil {
		return nil, nil
	}

	ip := net.ParseIP(publicIP)
	if ip == nil {
		s.log.WarnContext(ctx, "invalid IP in latitude_ip_shards table", "shard", shard, "ip", publicIP)
		return nil, nil
	}

	return []dns.RR{
		&dns.A{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 300},
			A:   ip.To4(),
		},
	}, nil
}

// lookupLobbyA returns an A record for the lobby IP (apex domain, xterm, shelley).
func (s *Server) lookupLobbyA(fqdn string, class uint16) ([]dns.RR, error) {
	if !s.lobbyIP.IsValid() {
		return nil, nil
	}
	return []dns.RR{
		&dns.A{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 300},
			A:   s.lobbyIP.AsSlice(),
		},
	}, nil
}

// lookupMetadataA returns an A record for the metadata IP (169.254.169.254).
// Used for integration proxy domains (*.int.{boxHost}).
func (s *Server) lookupMetadataA(fqdn string, class uint16) ([]dns.RR, error) {
	return []dns.RR{
		&dns.A{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 300},
			A:   net.ParseIP("169.254.169.254").To4(),
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

	// Skip shard names (s001, s002, n001, etc.)
	if _, err := parseShardFromName(qname); err == nil {
		return nil, nil
	}
	if _, err := parseLatitudeShardFromName(qname); err == nil {
		return nil, nil
	}

	row, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetIPShardAndUserGLBByBoxName, boxName)
	if err != nil {
		// No record found
		return nil, nil
	}

	shardSub := publicips.ShardSub(int(row.IPShard))
	if row.GlobalLoadBalancer != nil && *row.GlobalLoadBalancer != 0 {
		shardSub = publicips.LatitudeShardSub(int(row.IPShard))
	} else if row.GlobalLoadBalancer == nil && s.userMatchesGLBPrefix(row.CreatedByUserID) {
		shardSub = publicips.LatitudeShardSub(int(row.IPShard))
	}
	target := shardSub + "." + domain + "."

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

// lookupNS handles NS record queries.
// Returns NS records for the boxHost domain (e.g., exe.xyz -> ns1.exe.dev, ns2.exe.dev).
func (s *Server) lookupNS(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Only return NS records for the exact boxHost domain
	if qname != s.boxHost {
		return nil, nil
	}

	return []dns.RR{
		&dns.NS{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 86400},
			Ns:  "ns1." + s.webHost + ".",
		},
		&dns.NS{
			Hdr: dns.Header{Name: fqdn, Class: class, TTL: 86400},
			Ns:  "ns2." + s.webHost + ".",
		},
	}, nil
}

// lookupMX handles MX record queries for VM subdomains.
// Only returns MX for boxes with email_receive_enabled=1.
// MX points to mail.{boxHost} (e.g., mail.exe.xyz).
func (s *Server) lookupMX(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Only handle subdomains of boxHost (not the apex)
	suffix := "." + s.boxHost
	if !strings.HasSuffix(qname, suffix) {
		return nil, nil
	}

	// Extract box name (everything before the domain suffix)
	boxName := strings.TrimSuffix(qname, suffix)
	if boxName == "" || strings.Contains(boxName, ".") {
		// Empty name (apex) or nested subdomain - no MX
		return nil, nil
	}

	// Check if box exists and has email receive enabled
	_, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetBoxByNameWithEmailReceiveEnabled, boxName)
	if err != nil {
		// Box not found or email not enabled
		return nil, nil
	}

	// Return MX pointing to mail.{boxHost}
	return []dns.RR{
		&dns.MX{
			Hdr:        dns.Header{Name: fqdn, Class: class, TTL: 300},
			Preference: 10,
			Mx:         "mail." + s.boxHost + ".",
		},
	}, nil
}

// lookupSOA handles SOA record queries.
// Returns the SOA record for the boxHost domain (e.g., exe.xyz).
func (s *Server) lookupSOA(ctx context.Context, qname, fqdn string, class uint16) ([]dns.RR, error) {
	// Only return SOA record for the exact boxHost domain
	if qname != s.boxHost {
		return nil, nil
	}

	return []dns.RR{
		&dns.SOA{
			Hdr:     dns.Header{Name: fqdn, Class: class, TTL: 86400},
			Ns:      "ns1." + s.webHost + ".",
			Mbox:    "hostmaster." + s.webHost + ".",
			Serial:  1,       // Static serial; we don't do zone transfers
			Refresh: 86400,   // 1 day
			Retry:   7200,    // 2 hours
			Expire:  1209600, // 2 weeks
			Minttl:  300,     // 5 minutes (negative cache TTL)
		},
	}, nil
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

// parseLatitudeShardFromName extracts the latitude shard number from a DNS name.
// Returns error if the name is not a latitude shard name (e.g., "n043.exe.xyz" -> 43).
func parseLatitudeShardFromName(name string) (int, error) {
	parts := strings.SplitN(name, ".", 2)
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid name")
	}
	sub := parts[0]
	if len(sub) != 4 || sub[0] != 'n' {
		return 0, fmt.Errorf("not a latitude shard name")
	}
	shard, err := strconv.Atoi(sub[1:])
	if err != nil || shard < 1 || shard > publicips.MaxDomainShards {
		return 0, fmt.Errorf("invalid shard number")
	}
	return shard, nil
}
