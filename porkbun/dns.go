package porkbun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// DNSProvider implements DNS management for Porkbun
type DNSProvider struct {
	APIKey       string
	SecretAPIKey string
	HTTPClient   *http.Client
}

// NewDNSProvider creates a new Porkbun DNS provider
func NewDNSProvider(apiKey, secretAPIKey string) *DNSProvider {
	return &DNSProvider{
		APIKey:       apiKey,
		SecretAPIKey: secretAPIKey,
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// DNSRecord represents a Porkbun DNS record
type DNSRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     string `json:"ttl"`
	Prio    string `json:"prio"`
	Notes   string `json:"notes"`
}

// APIRequest represents the structure for Porkbun API requests
type APIRequest struct {
	SecretAPIKey string `json:"secretapikey"`
	APIKey       string `json:"apikey"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type,omitempty"`
	Content      string `json:"content,omitempty"`
	TTL          string `json:"ttl,omitempty"`
	Prio         string `json:"prio,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

// APIResponse represents the structure for Porkbun API responses
type APIResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message,omitempty"`
	ID      interface{} `json:"id,omitempty"` // Can be string or number
	Records []DNSRecord `json:"records,omitempty"`
}

// GetIDAsString returns the ID as a string, handling both string and number types
func (r *APIResponse) GetIDAsString() string {
	if r.ID == nil {
		return ""
	}
	switch id := r.ID.(type) {
	case string:
		return id
	case float64:
		return fmt.Sprintf("%.0f", id)
	case int:
		return fmt.Sprintf("%d", id)
	default:
		return fmt.Sprintf("%v", id)
	}
}

// CreateTXTRecord creates a TXT record for ACME challenge
func (d *DNSProvider) CreateTXTRecord(ctx context.Context, domain, name, content string) (string, error) {
	log.Printf("[DNS] CreateTXTRecord called: domain=%s, name=%s, content=%s", domain, name, content)

	req := APIRequest{
		SecretAPIKey: d.SecretAPIKey,
		APIKey:       d.APIKey,
		Name:         name,
		Type:         "TXT",
		Content:      content,
		TTL:          "600",
	}

	url := fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/create/%s", domain)
	log.Printf("[DNS] Making Porkbun API request to: %s", url)
	log.Printf("[DNS] Request payload: APIKey=%s, Name=%s, Type=%s, Content=%s, TTL=%s",
		req.APIKey, req.Name, req.Type, req.Content, req.TTL)

	resp, err := d.makeRequest(ctx, url, req)
	if err != nil {
		log.Printf("[DNS] Porkbun API request failed: %v", err)
		return "", fmt.Errorf("failed to create TXT record: %w", err)
	}

	log.Printf("[DNS] Porkbun API response: Status=%s, Message=%s, ID=%v",
		resp.Status, resp.Message, resp.ID)

	recordID := resp.GetIDAsString()
	log.Printf("[DNS] Successfully created TXT record with ID: %s", recordID)
	return recordID, nil
}

// DeleteRecord deletes a DNS record by ID
func (d *DNSProvider) DeleteRecord(ctx context.Context, domain, recordID string) error {
	req := APIRequest{
		SecretAPIKey: d.SecretAPIKey,
		APIKey:       d.APIKey,
	}

	url := fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/delete/%s/%s", domain, recordID)
	_, err := d.makeRequest(ctx, url, req)
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}
	return nil
}

// GetRecords retrieves all DNS records for a domain
func (d *DNSProvider) GetRecords(ctx context.Context, domain string) ([]DNSRecord, error) {
	req := APIRequest{
		SecretAPIKey: d.SecretAPIKey,
		APIKey:       d.APIKey,
	}

	url := fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/retrieve/%s", domain)
	resp, err := d.makeRequest(ctx, url, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get records: %w", err)
	}

	return resp.Records, nil
}

// FindTXTRecord finds a TXT record by name and content
func (d *DNSProvider) FindTXTRecord(ctx context.Context, domain, name, content string) (*DNSRecord, error) {
	records, err := d.GetRecords(ctx, domain)
	if err != nil {
		return nil, err
	}

	expectedName := name + "." + domain
	for _, record := range records {
		if record.Name == expectedName && record.Type == "TXT" && record.Content == content {
			return &record, nil
		}
	}

	return nil, fmt.Errorf("TXT record not found: %s", name)
}

// makeRequest makes an HTTP request to the Porkbun API
func (d *DNSProvider) makeRequest(ctx context.Context, url string, req APIRequest) (*APIResponse, error) {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	// Read the response body for debugging
	var responseBody bytes.Buffer
	if _, err := responseBody.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("[DNS] Raw Porkbun API response: %s", responseBody.String())

	var apiResp APIResponse
	if err := json.Unmarshal(responseBody.Bytes(), &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w (response body: %s)", err, responseBody.String())
	}

	if apiResp.Status != "SUCCESS" {
		return nil, fmt.Errorf("API error: %s (response body: %s)", apiResp.Message, responseBody.String())
	}

	return &apiResp, nil
}

// extractDomain extracts the base domain from a FQDN
// For wildcard certificates, we need to extract the domain that's registered with Porkbun
func extractDomain(fqdn string) string {
	parts := strings.Split(fqdn, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return fqdn
}

// CreateACMEChallenge creates a DNS TXT record for ACME DNS-01 challenge
func (d *DNSProvider) CreateACMEChallenge(ctx context.Context, domain, keyAuth string) (string, error) {
	// Extract the base domain (e.g., "exe.dev" from "*.exe.dev")
	baseDomain := extractDomain(domain)
	log.Printf("[DNS] CreateACMEChallenge: domain=%s, baseDomain=%s", domain, baseDomain)

	// Create the challenge record name
	var challengeName string
	if strings.HasPrefix(domain, "*.") {
		// For wildcard domain like "*.exe.dev", the challenge should be "_acme-challenge"
		// The DNS-01 challenge for *.domain.com goes to _acme-challenge.domain.com
		challengeName = "_acme-challenge"
		log.Printf("[DNS] Wildcard domain processing: challengeName=%s", challengeName)
	} else {
		// For regular domain or subdomain, create appropriate challenge name
		if domain == baseDomain {
			// For base domain like "exe.dev", create "_acme-challenge"
			challengeName = "_acme-challenge"
			log.Printf("[DNS] Base domain processing: challengeName=%s", challengeName)
		} else {
			// For subdomain like "user.exe.dev", create "_acme-challenge.user"
			subdomain := strings.TrimSuffix(domain, "."+baseDomain)
			challengeName = "_acme-challenge." + subdomain
			log.Printf("[DNS] Subdomain processing: subdomain=%s, challengeName=%s", subdomain, challengeName)
		}
	}

	log.Printf("[DNS] Calling CreateTXTRecord: baseDomain=%s, challengeName=%s, keyAuth=%s", baseDomain, challengeName, keyAuth)
	return d.CreateTXTRecord(ctx, baseDomain, challengeName, keyAuth)
}

// CleanupACMEChallenge removes the DNS TXT record after ACME challenge
func (d *DNSProvider) CleanupACMEChallenge(ctx context.Context, domain, keyAuth string) error {
	// Extract the base domain
	baseDomain := extractDomain(domain)

	// Create the challenge record name (same logic as CreateACMEChallenge)
	var challengeName string
	if strings.HasPrefix(domain, "*.") {
		// For wildcard domain like "*.exe.dev", the challenge should be "_acme-challenge"
		challengeName = "_acme-challenge"
	} else {
		// For regular domain or subdomain, create appropriate challenge name
		if domain == baseDomain {
			// For base domain like "exe.dev", create "_acme-challenge"
			challengeName = "_acme-challenge"
		} else {
			// For subdomain like "user.exe.dev", create "_acme-challenge.user"
			subdomain := strings.TrimSuffix(domain, "."+baseDomain)
			challengeName = "_acme-challenge." + subdomain
		}
	}

	// Find the record
	record, err := d.FindTXTRecord(ctx, baseDomain, challengeName, keyAuth)
	if err != nil {
		return fmt.Errorf("failed to find TXT record for cleanup: %w", err)
	}

	// Delete the record
	return d.DeleteRecord(ctx, baseDomain, record.ID)
}
