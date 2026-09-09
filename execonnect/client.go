package execonnect

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ExedClient owns an ExternalConnectionService client and its underlying gRPC
// connection.
type ExedClient struct {
	connectapi.ExternalConnectionServiceClient
	connection *grpc.ClientConn
}

// DialExed connects to exed's public HTTPS gRPC service.
func DialExed(rawURL string) (*ExedClient, error) {
	return dialExed(rawURL, nil)
}

func dialExed(rawURL string, roots *x509.CertPool) (*ExedClient, error) {
	parsed, err := parseExedURL(rawURL)
	if err != nil {
		return nil, err
	}
	connection, err := grpc.NewClient(
		parsed.Host,
		grpc.WithTransportCredentials(credentials.NewTLS(exedTLSConfig(parsed.Hostname(), roots))),
	)
	if err != nil {
		return nil, fmt.Errorf("create exed gRPC client: %w", err)
	}
	return &ExedClient{
		ExternalConnectionServiceClient: connectapi.NewExternalConnectionServiceClient(connection),
		connection:                      connection,
	}, nil
}

// Close closes the underlying gRPC connection.
func (client *ExedClient) Close() error {
	if client == nil || client.connection == nil {
		return nil
	}
	return client.connection.Close()
}

// NormalizeExedURL validates and canonicalizes an exed HTTPS URL.
func NormalizeExedURL(rawURL string) (string, error) {
	if rawURL == "" || rawURL != strings.TrimSpace(rawURL) {
		return "", fmt.Errorf("invalid exed URL %q", rawURL)
	}
	if len(rawURL) > maxEndpointURLLength {
		return "", fmt.Errorf("exed URL is too long")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse exed URL: %w", err)
	}
	if strings.ToLower(parsed.Scheme) != "https" {
		return "", fmt.Errorf("exed URL must use https")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("exed URL must not include user information")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("exed URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(rawURL, "#") {
		return "", fmt.Errorf("exed URL must not include a query or fragment")
	}
	host, err := normalizeEndpointHost(parsed.Hostname())
	if err != nil {
		return "", fmt.Errorf("exed URL has invalid host: %w", err)
	}
	portText, explicitPort, err := endpointURLPort(parsed.Host)
	if err != nil {
		return "", fmt.Errorf("exed URL has invalid port: %w", err)
	}
	port := 443
	if explicitPort {
		parsedPort, err := strconv.ParseUint(portText, 10, 16)
		if err != nil || parsedPort == 0 {
			return "", fmt.Errorf("exed URL has invalid port %q", portText)
		}
		port = int(parsedPort)
	}
	urlHost := host
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Is6() {
		urlHost = "[" + host + "]"
	}
	normalized := "https://" + urlHost
	if port != 443 {
		normalized += ":" + strconv.Itoa(port)
	}
	return normalized, nil
}

func parseExedURL(rawURL string) (*url.URL, error) {
	normalized, err := NormalizeExedURL(rawURL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("parse normalized exed URL: %w", err)
	}
	return parsed, nil
}

func exedTLSConfig(serverName string, roots *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		RootCAs:    roots,
	}
}
