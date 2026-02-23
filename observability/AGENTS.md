## Visualization Preferences

Generally, prefer time series widgets.

All dashboards should typically have stage filters. Use additional
filters (like role or instance) as applicable.

## Prometheus Access

Prometheus is accessible at `http://mon:9090`. You can query it directly:

```bash
# Query metrics
curl -s "http://mon:9090/api/v1/query" --data-urlencode 'query=up' | jq .

# Check targets and their health
curl -s "http://mon:9090/api/v1/targets" | jq '.data.activeTargets[] | "\(.labels.job) \(.labels.instance) \(.health)"'

# Get Prometheus config
curl -s "http://mon:9090/api/v1/status/config" | jq .
```

prometheus.yml can point you to where the prometheus metrics are

## SSH Access

if you need to ssh to a relevant host, use ubuntu@ as the user,
and you typically have SSH access

## Grafana Alerting API

To access Grafana alerting APIs, use the GRAFANA_BEARER_TOKEN environment variable.
The token works with Node.js fetch but may not work with curl directly due to auth handling.

Use the dashboards.mts script's pattern to access APIs:

```typescript
const response = await fetch(
  `${GRAFANA_URL}api/v1/provisioning/alert-rules/${alertUID}`,
  {
    headers: {
      Authorization: `Bearer ${TOKEN}`,
    },
  }
);
```

Key API endpoints:
- `GET/DELETE api/v1/provisioning/alert-rules/{uid}` - Get or delete a specific alert rule
- `POST api/v1/provisioning/alert-rules` - Create an alert rule
- `GET api/alertmanager/grafana/api/v2/alerts` - List currently firing alerts

### Clearing Stale Alert Instances

When an alert rule query changes to exclude certain series (e.g., filtering out NaN values
or excluding certain hosts), any existing firing alert instances for those series will NOT
automatically clear. They remain in the firing state with stale data.

To clear stale alert instances, you must DELETE the alert rule and recreate it:

```typescript
// Delete the rule to clear all alert state
await fetch(`${GRAFANA_URL}api/v1/provisioning/alert-rules/${alertUID}`, {
  method: "DELETE",
  headers: { Authorization: `Bearer ${TOKEN}` },
});

// Then redeploy to recreate the rule fresh
// make deploy-grafana
```

## Honeycomb MCP Access

Honeycomb logs can be queried via the Honeycomb MCP server using `mcp-cli`.

### First-time auth setup

Run the auth script:

```bash
./observability/honeycomb-mcp-auth.sh
```

This will:
1. Register a dynamic OAuth client with Honeycomb
2. Print an authorization URL — open it in your browser
3. After authorizing, your browser redirects to `localhost:9876` (which won't load on a VM — that's OK)
4. Copy the full URL from the browser address bar and paste it back into the script
5. The script exchanges the code for a token and saves it to `~/.config/mcp/mcp_servers.json`

The config ends up looking like:

```json
{
  "mcpServers": {
    "honeycomb": {
      "url": "https://mcp.honeycomb.io/mcp",
      "headers": {
        "Authorization": "Bearer <token>"
      }
    }
  }
}
```

### Using mcp-cli with Honeycomb

```bash
# List available tools
mcp-cli -c ~/.config/mcp/mcp_servers.json

# Get workspace context (environments, datasets)
mcp-cli call honeycomb get_workspace_context '{}'

# Get environment details
mcp-cli call honeycomb get_environment '{"environment_slug": "production"}'

# Run a query
mcp-cli call honeycomb run_query '{
  "environment": "production",
  "dataset": "exed",
  "query_spec": {
    "calculations": [{"op": "COUNT"}],
    "breakdowns": ["body"],
    "time_range": 86400,
    "orders": [{"op": "COUNT", "order": "descending"}],
    "limit": 20
  }
}'
```

Tokens expire; re-run `./observability/honeycomb-mcp-auth.sh` if you get 401s.
