package container

import (
	"reflect"
	"testing"
)

func TestBuildArgs_WithExetini_ImageEntrypointAndCmd(t *testing.T) {
	got := buildEntrypointAndCmdArgs(true, "", []string{"docker-entrypoint.sh"}, []string{"node"})
	want := []string{"-g", "--", "docker-entrypoint.sh", "node"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_WithExetini_NoEntrypointOrCmd(t *testing.T) {
	got := buildEntrypointAndCmdArgs(true, "", nil, nil)
	want := []string{"-g", "--", "sleep", "infinity"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_NoExetini_WithOverride(t *testing.T) {
	got := buildEntrypointAndCmdArgs(false, "echo hi", nil, nil)
	want := []string{"echo", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildArgs_NoExetini_NoneOrExeuntu(t *testing.T) {
	got1 := buildEntrypointAndCmdArgs(false, "none", nil, nil)
	want := []string{"sleep", "infinity"}
	if !reflect.DeepEqual(got1, want) {
		t.Fatalf("none override: got %v, want %v", got1, want)
	}
	// No longer testing isExeuntu since that parameter was removed
}

func TestChooseBestPortToRoute(t *testing.T) {
	tests := []struct {
		name         string
		exposedPorts map[string]struct{}
		expected     int
	}{
		{
			name:         "no exposed ports",
			exposedPorts: map[string]struct{}{},
			expected:     0,
		},
		{
			name: "only tcp/80",
			exposedPorts: map[string]struct{}{
				"80/tcp": {},
			},
			expected: 80,
		},
		{
			name: "tcp/80 with other ports",
			exposedPorts: map[string]struct{}{
				"3000/tcp": {},
				"80/tcp":   {},
				"5432/tcp": {},
			},
			expected: 80,
		},
		{
			name: "no tcp/80, has port >= 1024",
			exposedPorts: map[string]struct{}{
				"8080/tcp": {},
				"3000/tcp": {},
				"5432/tcp": {},
			},
			expected: 3000, // smallest port >= 1024
		},
		{
			name: "hashicorp/http-echo case",
			exposedPorts: map[string]struct{}{
				"5678/tcp": {},
			},
			expected: 5678,
		},
		{
			name: "only ports < 1024",
			exposedPorts: map[string]struct{}{
				"443/tcp": {},
				"22/tcp":  {},
				"21/tcp":  {},
			},
			expected: 0, // no ports >= 1024
		},
		{
			name: "mixed TCP and UDP ports",
			exposedPorts: map[string]struct{}{
				"8080/tcp": {},
				"53/udp":   {},
				"3000/tcp": {},
				"1234/udp": {},
			},
			expected: 3000, // only TCP ports considered
		},
		{
			name: "only UDP ports",
			exposedPorts: map[string]struct{}{
				"53/udp":   {},
				"1234/udp": {},
			},
			expected: 0, // no TCP ports
		},
		{
			name: "invalid port format",
			exposedPorts: map[string]struct{}{
				"invalid/tcp": {},
				"8080/tcp":    {},
			},
			expected: 8080, // ignores invalid port
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ChooseBestPortToRoute(tt.exposedPorts)
			if result != tt.expected {
				t.Errorf("ChooseBestPortToRoute() = %d, expected %d", result, tt.expected)
			}
		})
	}
}

// TestAutomaticRoutingScenarios tests various real-world scenarios for automatic routing
func TestAutomaticRoutingScenarios(t *testing.T) {
	scenarios := []struct {
		name         string
		imageName    string
		exposedPorts map[string]struct{}
		expectedPort int
		description  string
	}{
		{
			name:      "nginx image",
			imageName: "nginx:latest",
			exposedPorts: map[string]struct{}{
				"80/tcp": {},
			},
			expectedPort: 80,
			description:  "nginx exposes port 80, should get automatic routing to port 80",
		},
		{
			name:      "hashicorp/http-echo",
			imageName: "hashicorp/http-echo:latest",
			exposedPorts: map[string]struct{}{
				"5678/tcp": {},
			},
			expectedPort: 5678,
			description:  "hashicorp/http-echo exposes port 5678, should get automatic routing to port 5678",
		},
		{
			name:      "multi-port app with 80",
			imageName: "webapp:latest",
			exposedPorts: map[string]struct{}{
				"80/tcp":   {},
				"3000/tcp": {},
				"8080/tcp": {},
			},
			expectedPort: 80,
			description:  "app with multiple ports including 80, should prefer port 80",
		},
		{
			name:      "app without port 80",
			imageName: "api:latest",
			exposedPorts: map[string]struct{}{
				"8080/tcp": {},
				"3000/tcp": {},
				"9000/tcp": {},
			},
			expectedPort: 3000,
			description:  "app without port 80, should choose smallest port >= 1024",
		},
		{
			name:         "app with no exposed ports",
			imageName:    "basic:latest",
			exposedPorts: map[string]struct{}{},
			expectedPort: 0,
			description:  "app with no exposed ports, should not set up routing",
		},
		{
			name:      "database with standard port",
			imageName: "postgres:latest",
			exposedPorts: map[string]struct{}{
				"5432/tcp": {},
			},
			expectedPort: 5432,
			description:  "postgres exposes port 5432, should get automatic routing to port 5432",
		},
		{
			name:      "redis with default port",
			imageName: "redis:latest",
			exposedPorts: map[string]struct{}{
				"6379/tcp": {},
			},
			expectedPort: 6379,
			description:  "redis exposes port 6379, should get automatic routing to port 6379",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			detectedPort := ChooseBestPortToRoute(scenario.exposedPorts)
			if detectedPort != scenario.expectedPort {
				t.Errorf("Scenario '%s': ChooseBestPortToRoute() = %d, expected %d\nDescription: %s",
					scenario.name, detectedPort, scenario.expectedPort, scenario.description)
			}
		})
	}
}
