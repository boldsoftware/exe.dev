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
