package route53

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awsroute53 "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

// DNSProvider implements DNS management for Route53
type DNSProvider struct {
	HTTPClient *http.Client

	client    *awsroute53.Client
	zoneCache map[string]string
	mu        sync.Mutex
}

// NewDNSProvider creates a new Route53 DNS provider
func NewDNSProvider() *DNSProvider {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithHTTPClient(httpClient),
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to load AWS configuration for Route53: %v", err))
	}

	return &DNSProvider{
		HTTPClient: httpClient,
		client:     awsroute53.NewFromConfig(cfg),
		zoneCache:  make(map[string]string),
	}
}

// DNSRecord represents a Route53 DNS record
type DNSRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     string `json:"ttl"`
	Prio    string `json:"prio"`
	Notes   string `json:"notes"`
}

type recordIdentifier struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	TTL    int64    `json:"ttl"`
	Values []string `json:"values"`
}

// CNAMERecord represents a Route53 CNAME record
type CNAMERecord struct {
	Name   string
	Target string
	TTL    int64
}

func encodeRecordID(name, recordType string, ttl int64, values []string) (string, error) {
	raw, err := json.Marshal(recordIdentifier{
		Name:   name,
		Type:   recordType,
		TTL:    ttl,
		Values: values,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func decodeRecordID(id string) (*recordIdentifier, error) {
	raw, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("failed to decode record id: %w", err)
	}
	var ident recordIdentifier
	if err := json.Unmarshal(raw, &ident); err != nil {
		return nil, fmt.Errorf("failed to parse record id: %w", err)
	}
	if ident.Name == "" || ident.Type == "" {
		return nil, fmt.Errorf("record id missing required fields")
	}
	return &ident, nil
}

func (d *DNSProvider) getHostedZoneID(ctx context.Context, domain string) (string, error) {
	baseDomain := extractDomain(domain)
	// NOTE: We currently keep every delegated name under a single hosted zone per
	// apex (e.g. exe.dev). If we ever break sub-zones like xterm.exe.dev into
	// their own Route53 zones, this lookup must be revisited to walk the labels
	// until it finds the correct hosted zone.

	d.mu.Lock()
	if zoneID, ok := d.zoneCache[baseDomain]; ok {
		d.mu.Unlock()
		return zoneID, nil
	}
	d.mu.Unlock()

	searchName := ensureTrailingDot(baseDomain)
	resp, err := d.client.ListHostedZonesByName(ctx, &awsroute53.ListHostedZonesByNameInput{
		DNSName: aws.String(searchName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list hosted zones: %w", err)
	}

	for _, zone := range resp.HostedZones {
		if strings.TrimSuffix(aws.ToString(zone.Name), ".") == baseDomain {
			zoneID := strings.TrimPrefix(aws.ToString(zone.Id), "/hostedzone/")
			d.mu.Lock()
			d.zoneCache[baseDomain] = zoneID
			d.mu.Unlock()
			return zoneID, nil
		}
	}

	return "", fmt.Errorf("hosted zone not found for domain %s", baseDomain)
}

func ensureTrailingDot(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func buildRecordName(domain, name string) string {
	if name == "" || name == domain {
		return domain
	}
	if strings.HasSuffix(name, "."+domain) {
		return name
	}
	return name + "." + domain
}

// CreateTXTRecords creates TXT records for ACME challenges
func (d *DNSProvider) CreateTXTRecords(ctx context.Context, domain, name string, contents []string) (string, error) {
	slog.Info("[DNS] CreateTXTRecords called", "domain", domain, "name", name, "contents", contents)

	zoneID, err := d.getHostedZoneID(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("failed to resolve hosted zone: %w", err)
	}

	recordName := buildRecordName(domain, name)
	fqdn := ensureTrailingDot(recordName)
	ttl := int64(600)

	resourceRecords := make([]types.ResourceRecord, 0, len(contents))
	for _, content := range contents {
		resourceRecords = append(resourceRecords, types.ResourceRecord{
			Value: aws.String(fmt.Sprintf("%q", content)),
		})
	}

	slog.Info("[DNS] Upserting TXT record in Route53", "zoneID", zoneID, "name", recordName)
	_, err = d.client.ChangeResourceRecordSets(ctx, &awsroute53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            aws.String(fqdn),
						Type:            types.RRTypeTxt,
						TTL:             aws.Int64(ttl),
						ResourceRecords: resourceRecords,
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to upsert TXT record: %w", err)
	}

	recordID, err := encodeRecordID(recordName, "TXT", ttl, contents)
	if err != nil {
		return "", fmt.Errorf("failed to encode record id: %w", err)
	}
	slog.Info("[DNS] Successfully upserted TXT record with synthetic ID", "recordID", recordID)

	return recordID, nil
}

// DeleteRecord deletes a DNS record by ID
func (d *DNSProvider) DeleteRecord(ctx context.Context, domain, recordID string) error {
	zoneID, err := d.getHostedZoneID(ctx, domain)
	if err != nil {
		return fmt.Errorf("failed to resolve hosted zone: %w", err)
	}
	ident, err := decodeRecordID(recordID)
	if err != nil {
		return fmt.Errorf("invalid record id: %w", err)
	}

	recordName := buildRecordName(domain, ident.Name)
	fqdn := ensureTrailingDot(recordName)

	var records []types.ResourceRecord
	for _, value := range ident.Values {
		records = append(records, types.ResourceRecord{
			Value: aws.String(fmt.Sprintf("\"%s\"", value)),
		})
	}
	// TODO: Route53 expects bare domain names for non-TXT records (e.g. CNAME targets), so
	// quoting everything here prevents DeleteRecord from working for those types. Preserve
	// whatever format encodeRecordID stored instead of always adding quotes.

	ttl := ident.TTL
	if ttl == 0 {
		ttl = 600
	}

	rrType := types.RRType(ident.Type)

	_, err = d.client.ChangeResourceRecordSets(ctx, &awsroute53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: types.ChangeActionDelete,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name:            aws.String(fqdn),
						Type:            rrType,
						TTL:             aws.Int64(ttl),
						ResourceRecords: records,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}

	return nil
}

// ListCNAMERecords returns all CNAME records for the provided domain.
func (d *DNSProvider) ListCNAMERecords(ctx context.Context, domain string) ([]CNAMERecord, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}

	zoneID, err := d.getHostedZoneID(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hosted zone: %w", err)
	}

	paginator := awsroute53.NewListResourceRecordSetsPaginator(d.client, &awsroute53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})

	var records []CNAMERecord
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list CNAME record sets: %w", err)
		}

		for _, rrset := range page.ResourceRecordSets {
			if rrset.Type != types.RRTypeCname {
				continue
			}
			if len(rrset.ResourceRecords) == 0 {
				// TODO: This fails the entire listing if one CNAME is malformed.
				// Consider skipping invalid records instead of aborting.
				return nil, fmt.Errorf("CNAME record %s has no target", aws.ToString(rrset.Name))
			}

			name := strings.TrimSuffix(strings.ToLower(aws.ToString(rrset.Name)), ".")
			target := strings.TrimSuffix(strings.Trim(strings.ToLower(aws.ToString(rrset.ResourceRecords[0].Value)), "\""), ".")

			var ttl int64
			if rrset.TTL != nil {
				ttl = aws.ToInt64(rrset.TTL)
			}

			records = append(records, CNAMERecord{
				Name:   name,
				Target: strings.ToLower(target),
				TTL:    ttl,
			})
		}
	}

	return records, nil
}

// CreateCNAMERecord creates or updates a CNAME record for the provided host.
func (d *DNSProvider) CreateCNAMERecord(ctx context.Context, domain, name, target string, ttl time.Duration) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", fmt.Errorf("record name is required")
	}
	if ttl <= 0 {
		return "", fmt.Errorf("ttl must be greater than zero")
	}
	if ttl%time.Second != 0 {
		return "", fmt.Errorf("ttl must be a whole number of seconds")
	}

	targetValue, err := normalizeCNAMEValue(domain, target)
	if err != nil {
		return "", err
	}

	zoneID, err := d.getHostedZoneID(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("failed to resolve hosted zone: %w", err)
	}

	recordName := buildRecordName(domain, name)
	fqdn := ensureTrailingDot(recordName)
	targetFQDN := ensureTrailingDot(targetValue)
	ttlSeconds := int64(ttl / time.Second)

	_, err = d.client.ChangeResourceRecordSets(ctx, &awsroute53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &types.ChangeBatch{
			Changes: []types.Change{
				{
					Action: types.ChangeActionUpsert,
					ResourceRecordSet: &types.ResourceRecordSet{
						Name: aws.String(fqdn),
						Type: types.RRTypeCname,
						TTL:  aws.Int64(ttlSeconds),
						ResourceRecords: []types.ResourceRecord{
							{Value: aws.String(targetFQDN)},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to upsert CNAME record: %w", err)
	}

	recordID, err := encodeRecordID(recordName, "CNAME", ttlSeconds, []string{targetValue})
	if err != nil {
		return "", fmt.Errorf("failed to encode record id: %w", err)
	}
	return recordID, nil
}

func normalizeCNAMEValue(domain, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("target is required")
	}
	target = strings.TrimSuffix(target, ".")

	targetLower := strings.ToLower(target)
	domainLower := strings.ToLower(domain)

	if strings.Contains(targetLower, ".") && !strings.HasSuffix(targetLower, "."+domainLower) && targetLower != domainLower {
		return targetLower, nil
	}
	return strings.ToLower(buildRecordName(domainLower, targetLower)), nil
}

// GetRecords retrieves all DNS records for a domain
func (d *DNSProvider) GetRecords(ctx context.Context, domain string) ([]DNSRecord, error) {
	zoneID, err := d.getHostedZoneID(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hosted zone: %w", err)
	}

	var records []DNSRecord
	paginator := awsroute53.NewListResourceRecordSetsPaginator(d.client, &awsroute53.ListResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list resource record sets: %w", err)
		}

		for _, rrset := range page.ResourceRecordSets {
			ttl := int64(0)
			if rrset.TTL != nil {
				ttl = aws.ToInt64(rrset.TTL)
			}
			name := strings.TrimSuffix(aws.ToString(rrset.Name), ".")
			recordType := string(rrset.Type)

			var values []string
			for _, rr := range rrset.ResourceRecords {
				values = append(values, strings.Trim(aws.ToString(rr.Value), "\""))
			}

			recordID, err := encodeRecordID(name, recordType, ttl, values)
			if err != nil {
				return nil, fmt.Errorf("failed to encode record id: %w", err)
			}

			if len(values) == 0 {
				records = append(records, DNSRecord{
					ID:   recordID,
					Name: name,
					Type: recordType,
					TTL:  fmt.Sprintf("%d", ttl),
				})
				continue
			}

			for _, value := range values {
				records = append(records, DNSRecord{
					ID:      recordID,
					Name:    name,
					Type:    recordType,
					Content: value,
					TTL:     fmt.Sprintf("%d", ttl),
				})
			}
		}
	}

	return records, nil
}

// FindTXTRecord finds a TXT record by name and content
func (d *DNSProvider) FindTXTRecord(ctx context.Context, domain, name, content string) (*DNSRecord, error) {
	records, err := d.GetRecords(ctx, domain)
	if err != nil {
		return nil, err
	}

	expectedName := buildRecordName(domain, name)
	for _, record := range records {
		if record.Name == expectedName && record.Type == "TXT" && record.Content == content {
			return &record, nil
		}
	}

	return nil, fmt.Errorf("TXT record not found: %s", name)
}

// CreateACMEChallenge creates a DNS TXT record for ACME DNS-01 challenge
func (d *DNSProvider) CreateACMEChallenge(ctx context.Context, domain, keyAuth string) (string, error) {
	// Extract the base domain (e.g., "exe.dev" from "*.exe.dev")
	baseDomain := extractDomain(domain)
	slog.Info("[DNS] CreateACMEChallenge", "domain", domain, "baseDomain", baseDomain)

	challengeName := acmeChallengeName(domain)
	slog.Info("[DNS] Using ACME challenge name", "challengeName", challengeName, "domain", domain, "baseDomain", baseDomain)

	values := []string{keyAuth}
	// Keep any existing tokens for this challenge so multiple authorizations in the same order can coexist.
	records, err := d.GetRecords(ctx, baseDomain)
	if err != nil {
		slog.Warn("[DNS] failed to get existing records for cleanup", "error", err)
	} else {
		expectedName := buildRecordName(baseDomain, challengeName)
		for _, record := range records {
			if record.Name == expectedName && record.Type == "TXT" {
				values = append(values, record.Content)
			}
		}
	}

	// Deduplicate
	slices.Sort(values)
	values = slices.Compact(values)

	slog.Info("[DNS] Calling CreateTXTRecord", "baseDomain", baseDomain, "challengeName", challengeName, "keyAuth", keyAuth)
	return d.CreateTXTRecords(ctx, baseDomain, challengeName, values)
}

// CleanupACMEChallenge removes the DNS TXT record after ACME challenge
func (d *DNSProvider) CleanupACMEChallenge(ctx context.Context, domain, keyAuth string) error {
	// Extract the base domain
	baseDomain := extractDomain(domain)

	challengeName := acmeChallengeName(domain)

	// Find the record
	record, err := d.FindTXTRecord(ctx, baseDomain, challengeName, keyAuth)
	if err != nil {
		return fmt.Errorf("failed to find TXT record for cleanup: %w", err)
	}

	// Delete the record
	return d.DeleteRecord(ctx, baseDomain, record.ID)
}

// extractDomain extracts the base domain from a FQDN
// For wildcard certificates, we need to extract the domain that's registered with Route53
func extractDomain(fqdn string) string {
	parts := strings.Split(fqdn, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return fqdn
}

// acmeChallengeName returns the TXT record prefix for a domain (wildcard or otherwise).
func acmeChallengeName(domain string) string {
	baseDomain := extractDomain(domain)
	target := strings.TrimPrefix(domain, "*.")
	if target == baseDomain {
		// "*.exe.dev" -> "_acme-challenge"
		return "_acme-challenge"
	}

	sub, ok := strings.CutSuffix(target, "."+baseDomain)
	if ok {
		// "*.sub.exe.dev" -> "_acme-challenge.sub"
		return "_acme-challenge." + sub
	}

	// "exe.dev" -> "_acme-challenge"
	return "_acme-challenge"
}
