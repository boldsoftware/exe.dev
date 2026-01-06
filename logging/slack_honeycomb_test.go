package logging

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBuildHoneycombURL(t *testing.T) {
	eventTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	traceID := "d12224da402fac5d1aac3827bc3820dd"

	tests := []struct {
		name    string
		env     string
		wantEnv string
	}{
		{
			name:    "production",
			env:     "production",
			wantEnv: "production",
		},
		{
			name:    "staging",
			env:     "staging",
			wantEnv: "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildHoneycombURL(tt.env, traceID, eventTime)

			// Check base URL structure
			if !strings.HasPrefix(result, "https://ui.honeycomb.io/bold-00/environments/"+tt.wantEnv+"/datasets/exed?query=") {
				t.Errorf("URL has wrong base structure: %s", result)
			}

			// Parse the URL
			u, err := url.Parse(result)
			if err != nil {
				t.Fatalf("Failed to parse URL: %v", err)
			}

			// Get and decode the query parameter
			queryParam := u.Query().Get("query")
			if queryParam == "" {
				t.Fatal("Missing query parameter")
			}

			// Check that the trace_id filter is in the query
			if !strings.Contains(queryParam, traceID) {
				t.Errorf("Query does not contain trace_id %s: %s", traceID, queryParam)
			}

			// Check that the time window is correct (±10 minutes from 10:30 UTC)
			// 10:30 UTC on Jan 15, 2024 = 1705314600 unix timestamp
			// -10 min = 1705314000, +10 min = 1705315200
			expectedStartTime := eventTime.Add(-10 * time.Minute).Unix()
			expectedEndTime := eventTime.Add(10 * time.Minute).Unix()

			if !strings.Contains(queryParam, `"start_time":1705314000`) {
				t.Errorf("Query does not contain expected start_time %d: %s", expectedStartTime, queryParam)
			}
			if !strings.Contains(queryParam, `"end_time":1705315200`) {
				t.Errorf("Query does not contain expected end_time %d: %s", expectedEndTime, queryParam)
			}
		})
	}
}

func TestHoneycombConverterWithTraceID(t *testing.T) {
	SetHoneycombEnv("production")

	// We can't easily test the full converter without setting up slog,
	// but we can at least verify the environment is set correctly
	if honeycombEnv != "production" {
		t.Errorf("Expected honeycombEnv to be 'production', got %q", honeycombEnv)
	}
}
