package execonnect

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// EndpointRegistrySnapshot is one complete local endpoint registry.
type EndpointRegistrySnapshot struct {
	Endpoints []Endpoint
}

type endpointRegistryRecord struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	TLSServerName string `json:"tls_server_name,omitempty"`
}

func recordsFromEndpoints(endpoints []Endpoint) ([]endpointRegistryRecord, error) {
	if len(endpoints) > MaxEndpoints {
		return nil, fmt.Errorf("endpoint limit is %d", MaxEndpoints)
	}
	records := make([]endpointRegistryRecord, 0, len(endpoints))
	seen := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		normalized, err := NormalizeEndpoint(endpoint.Key, endpoint.URL, endpoint.TLSServerName)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized.Key]; ok {
			return nil, fmt.Errorf("duplicate endpoint name %q", normalized.Key)
		}
		seen[normalized.Key] = struct{}{}
		records = append(records, endpointRegistryRecord{
			Name:          normalized.Key,
			URL:           normalized.URL,
			TLSServerName: normalized.TLSServerName,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})
	return records, nil
}

func endpointsFromRecords(records []endpointRegistryRecord) ([]Endpoint, error) {
	if len(records) > MaxEndpoints {
		return nil, fmt.Errorf("endpoint limit is %d", MaxEndpoints)
	}
	endpoints := make([]Endpoint, 0, len(records))
	previousName := ""
	for index, record := range records {
		endpoint, err := NormalizeEndpoint(record.Name, record.URL, record.TLSServerName)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint entry %d: %w", index, err)
		}
		if endpoint.Key != record.Name || endpoint.URL != record.URL || endpoint.TLSServerName != record.TLSServerName {
			return nil, fmt.Errorf("endpoint entry %q is not canonical", record.Name)
		}
		if previousName != "" && endpoint.Key <= previousName {
			return nil, fmt.Errorf("endpoint entries must be uniquely sorted by name")
		}
		previousName = endpoint.Key
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}

func addEndpoint(snapshot EndpointRegistrySnapshot, name, rawURL, tlsServerName string) (EndpointRegistrySnapshot, bool, error) {
	endpoint, err := NormalizeEndpoint(name, rawURL, tlsServerName)
	if err != nil {
		return EndpointRegistrySnapshot{}, false, err
	}
	endpoints := append([]Endpoint(nil), snapshot.Endpoints...)
	index, found := endpointIndex(endpoints, endpoint.Key)
	if found {
		if endpoints[index] == endpoint {
			return snapshot, false, nil
		}
		return EndpointRegistrySnapshot{}, false, fmt.Errorf("endpoint %q already exists with a different URL; use 'execonnect endpoint update'", endpoint.Key)
	}
	if len(endpoints) >= MaxEndpoints {
		return EndpointRegistrySnapshot{}, false, fmt.Errorf("endpoint limit is %d", MaxEndpoints)
	}
	endpoints = append(endpoints, Endpoint{})
	copy(endpoints[index+1:], endpoints[index:])
	endpoints[index] = endpoint
	return EndpointRegistrySnapshot{Endpoints: endpoints}, true, nil
}

func updateEndpoint(snapshot EndpointRegistrySnapshot, name, rawURL, tlsServerName string) (EndpointRegistrySnapshot, bool, error) {
	endpoint, err := NormalizeEndpoint(name, rawURL, tlsServerName)
	if err != nil {
		return EndpointRegistrySnapshot{}, false, err
	}
	endpoints := append([]Endpoint(nil), snapshot.Endpoints...)
	index, found := endpointIndex(endpoints, endpoint.Key)
	if !found {
		return EndpointRegistrySnapshot{}, false, fmt.Errorf("endpoint %q does not exist; use 'execonnect endpoint add'", endpoint.Key)
	}
	if endpoints[index] == endpoint {
		return snapshot, false, nil
	}
	endpoints[index] = endpoint
	return EndpointRegistrySnapshot{Endpoints: endpoints}, true, nil
}

func removeEndpoint(snapshot EndpointRegistrySnapshot, name string) (EndpointRegistrySnapshot, error) {
	name = strings.TrimSpace(name)
	if !endpointKeyPattern.MatchString(name) {
		return EndpointRegistrySnapshot{}, fmt.Errorf("invalid endpoint name %q: use a 1-63 character lowercase DNS label", name)
	}
	endpoints := append([]Endpoint(nil), snapshot.Endpoints...)
	index, found := endpointIndex(endpoints, name)
	if !found {
		return EndpointRegistrySnapshot{}, fmt.Errorf("endpoint %q does not exist", name)
	}
	return EndpointRegistrySnapshot{Endpoints: slices.Delete(endpoints, index, index+1)}, nil
}

func copyEndpointRegistrySnapshot(snapshot EndpointRegistrySnapshot) EndpointRegistrySnapshot {
	return EndpointRegistrySnapshot{Endpoints: append([]Endpoint(nil), snapshot.Endpoints...)}
}

func endpointIndex(endpoints []Endpoint, name string) (int, bool) {
	return slices.BinarySearchFunc(endpoints, name, func(endpoint Endpoint, name string) int {
		return strings.Compare(endpoint.Key, name)
	})
}
