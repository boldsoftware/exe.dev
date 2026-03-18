package aiagent

// ToolDefs returns the tool definitions that the AI can call.
// All tools are read-only database queries.
func ToolDefs() []Tool {
	return []Tool{
		{
			Name:        "list_servers",
			Description: "List all servers in the fleet with their latest metrics including CPU, memory, disk, and network usage. Returns a summary for each server.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "get_server_details",
			Description: "Get detailed information about a specific server including full metrics, components, updates, failed systemd units, ZFS pools, and recent history (up to 1000 data points).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The server name (e.g. exelet-nyc-prod-01)",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "get_fleet_status",
			Description: "Get extended fleet-wide data for all servers, including components, updates, failed units, ZFS pools, conntrack, file descriptors, and network error counters.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// AllowedTools is the set of tool names the AI is permitted to call.
var AllowedTools = map[string]bool{
	"list_servers":       true,
	"get_server_details": true,
	"get_fleet_status":   true,
}
