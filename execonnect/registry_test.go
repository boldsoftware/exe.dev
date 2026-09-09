package execonnect

import (
	"strings"
	"testing"
)

func TestEndpointRecordsRoundTripInDeterministicOrder(t *testing.T) {
	second, err := NormalizeEndpoint("web", "https://web.internal", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := NormalizeEndpoint("database", "tcp://db.internal:5432", "")
	if err != nil {
		t.Fatal(err)
	}
	records, err := recordsFromEndpoints([]Endpoint{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Name != "database" || records[1].Name != "web" {
		t.Fatalf("records = %#v", records)
	}
	endpoints, err := endpointsFromRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	if endpoints[0] != first || endpoints[1] != second {
		t.Fatalf("endpoints = %#v", endpoints)
	}
}

func TestEndpointRecordsRejectNonCanonicalAndDuplicateData(t *testing.T) {
	for _, records := range [][]endpointRegistryRecord{
		{{Name: "web", URL: "HTTPS://WEB.INTERNAL:443/"}},
		{{Name: "web", URL: "https://web.internal"}, {Name: "web", URL: "https://web.internal"}},
	} {
		if _, err := endpointsFromRecords(records); err == nil {
			t.Fatalf("accepted records %#v", records)
		}
	}
}

func TestEndpointMutationsRetainExistingContracts(t *testing.T) {
	empty := EndpointRegistrySnapshot{Endpoints: []Endpoint{}}
	added, changed, err := addEndpoint(empty, "database", "tcp://db.internal:5432", "")
	if err != nil || !changed || len(added.Endpoints) != 1 {
		t.Fatalf("add = %#v, changed=%v, err=%v", added, changed, err)
	}
	if _, changed, err := addEndpoint(added, "database", "tcp://db.internal:5432", ""); err != nil || changed {
		t.Fatalf("idempotent add changed=%v err=%v", changed, err)
	}
	if _, _, err := addEndpoint(added, "database", "tcp://other.internal:5432", ""); err == nil || !strings.Contains(err.Error(), "use 'execonnect endpoint update'") {
		t.Fatalf("conflicting add error = %v", err)
	}
	updated, changed, err := updateEndpoint(added, "database", "tcp://db-new.internal:5432", "")
	if err != nil || !changed || updated.Endpoints[0].Target != "db-new.internal:5432" {
		t.Fatalf("update = %#v, changed=%v, err=%v", updated, changed, err)
	}
	removed, err := removeEndpoint(updated, "database")
	if err != nil || len(removed.Endpoints) != 0 {
		t.Fatalf("remove = %#v, err=%v", removed, err)
	}
}
