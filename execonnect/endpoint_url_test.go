package execonnect

import (
	"strings"
	"testing"

	connectapi "github.com/boldsoftware/exe.dev/execonnect/pkg/api/exe/connect/v1"
)

func TestNormalizeEndpointURL(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		rawURL     string
		tlsName    string
		wantURL    string
		wantTarget string
		wantTLS    string
		wantProto  connectapi.EndpointProtocol
	}{
		{
			name:       "http default port and lowercase host",
			rawURL:     "http://API.Internal.:80/",
			wantURL:    "http://api.internal",
			wantTarget: "api.internal:80",
			wantProto:  connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTP,
		},
		{
			name:       "https derived server name",
			rawURL:     "https://API.Internal",
			wantURL:    "https://api.internal",
			wantTarget: "api.internal:443",
			wantTLS:    "api.internal",
			wantProto:  connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTPS,
		},
		{
			name:       "https override",
			rawURL:     "https://10.0.0.1:8443",
			tlsName:    "SERVICE.Internal",
			wantURL:    "https://10.0.0.1:8443",
			wantTarget: "10.0.0.1:8443",
			wantTLS:    "service.internal",
			wantProto:  connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_HTTPS,
		},
		{
			name:       "canonical IPv6 tcp",
			rawURL:     "tcp://[2001:0db8::1]:05432",
			wantURL:    "tcp://[2001:db8::1]:5432",
			wantTarget: "[2001:db8::1]:5432",
			wantProto:  connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_TCP,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			endpoint, err := NormalizeEndpoint("service", testCase.rawURL, testCase.tlsName)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.Key != "service" || endpoint.DisplayName != "service" || endpoint.URL != testCase.wantURL || endpoint.Target != testCase.wantTarget || endpoint.Protocol != testCase.wantProto || endpoint.TLSServerName != testCase.wantTLS {
				t.Fatalf("endpoint = %#v", endpoint)
			}
		})
	}
}

func TestNormalizeEndpointRejectsInvalidDefinitions(t *testing.T) {
	longHost := strings.Repeat("a", maxEndpointHostLength+1)
	longURL := "https://" + strings.Repeat("a", maxEndpointURLLength)
	longName := strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name    string
		key     string
		rawURL  string
		tlsName string
	}{
		{name: "name too long", key: longName, rawURL: "tcp://db.internal:5432"},
		{name: "uppercase name", key: "Database", rawURL: "tcp://db.internal:5432"},
		{name: "userinfo", key: "db", rawURL: "tcp://user@db.internal:5432"},
		{name: "path", key: "db", rawURL: "https://db.internal/private"},
		{name: "empty query", key: "db", rawURL: "https://db.internal?"},
		{name: "query", key: "db", rawURL: "https://db.internal?x=1"},
		{name: "empty fragment", key: "db", rawURL: "https://db.internal#"},
		{name: "fragment", key: "db", rawURL: "https://db.internal#x"},
		{name: "tcp missing port", key: "db", rawURL: "tcp://db.internal"},
		{name: "zero port", key: "db", rawURL: "tcp://db.internal:0"},
		{name: "unbracketed ipv6", key: "db", rawURL: "tcp://2001:db8::1:5432"},
		{name: "non ascii host", key: "db", rawURL: "https://café.internal"},
		{name: "invalid dns label", key: "db", rawURL: "https://bad_name.internal"},
		{name: "unbounded URL", key: "db", rawURL: longURL},
		{name: "unbounded host", key: "db", rawURL: "https://" + longHost},
		{name: "unbounded TLS name", key: "db", rawURL: "https://db.internal", tlsName: longHost},
		{name: "http tls override", key: "db", rawURL: "http://db.internal", tlsName: "db.internal"},
		{name: "tcp tls metadata", key: "db", rawURL: "tcp://db.internal:443", tlsName: "db.internal"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NormalizeEndpoint(testCase.key, testCase.rawURL, testCase.tlsName); err == nil {
				t.Fatalf("NormalizeEndpoint(%q, %q, %q) succeeded", testCase.key, testCase.rawURL, testCase.tlsName)
			}
		})
	}
}

func TestBuildEndpointSnapshotPublishesKeyAsDisplayName(t *testing.T) {
	httpsEndpoint, err := NormalizeEndpoint("web", "https://web.internal", "")
	if err != nil {
		t.Fatal(err)
	}
	tcpEndpoint, err := NormalizeEndpoint("database", "tcp://db.internal:5432", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildEndpointSnapshot([]Endpoint{httpsEndpoint, tcpEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.GetEndpoints()) != 2 {
		t.Fatalf("endpoints = %#v", snapshot.GetEndpoints())
	}
	first := snapshot.GetEndpoints()[0]
	if first.GetKey() != "database" || first.GetDisplayName() != "database" || first.GetTargetHost() != "db.internal" || first.GetTargetPort() != 5432 || first.GetProtocol() != connectapi.EndpointProtocol_ENDPOINT_PROTOCOL_TCP || first.GetTlsServerName() != "" {
		t.Fatalf("first endpoint = %#v", first)
	}
	second := snapshot.GetEndpoints()[1]
	if second.GetKey() != "web" || second.GetDisplayName() != "web" || second.GetTargetPort() != 443 || second.GetTlsServerName() != "web.internal" {
		t.Fatalf("second endpoint = %#v", second)
	}
}
