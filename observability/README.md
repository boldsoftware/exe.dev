# Checked in Grafana Dashboards

Running "./node_modules/.bin/tsx dashboards.mts" will populate a dashboard in Grafana.

Doing this in typescript gives you working (mostly) types for Grafana stuff.

## Alert Support

The dashboard script supports creating alerts with threshold visualization.
You can add alerts to any chart by including an `alert` configuration:

```typescript
addTimeseriesChart(
  "Disk Usage %",
  `(1 - (node_filesystem_avail_bytes{${HOST_FILTER},fstype!="tmpfs",fstype!="devtmpfs"} / node_filesystem_size_bytes{${HOST_FILTER},fstype!="tmpfs",fstype!="devtmpfs"})) * 100`,
  {
    panelCustomization: (x) =>
      x.unit("percent").min(0).max(100).gridPos({ x: 0, y: 19, w: 8, h: 6 }),
    alert: {
      threshold: 80,
      condition: "gt", // required: "gt", "lt", "eq", "ne"
      forDuration: "1m",
      summary: "Disk usage is critically high",
      description: "Disk usage has exceeded 80% for more than 1 minute"
    }
  },
);
```

This will:
1. Add a dashed threshold line at 80% on the chart
2. Create a Grafana alert rule that fires when the metric exceeds 80% for more than 1 minute
3. Store the alert in the "Auto-Generated Alerts" folder in the "dashboard-alerts" rule group
4. Update existing alerts if they already exist (allowing configuration changes)
5. Use proper Grafana unified alerting with query → reduce → threshold evaluation

Alert configuration options:
- `threshold`: The numeric threshold value
- `condition`: **Required** - "gt" (greater than), "lt" (less than), "eq" (equal), "ne" (not equal)
- `forDuration`: How long the condition must persist before alerting (e.g., "1m", "5m", "10s")
- `summary`: Short summary for the alert
- `description`: Detailed description for the alert

# General Notes on Monitoring setup

prometheus-node-exporter runs on various machines and exposes at http://localhost:9100/metrics

Raw prometheus is at http://mon.crocodile-vector.ts.net:9090/graph

Grafana is at https://grafana.crocodile-vector.ts.net

Edit /home/ubuntu/prometheus/prometheus.yml (on mon) to add metrics for
prometheus to collect. Ran "sudo systemctl restart prometheus" on mon for good
measure.

docker metrics exist, but are a bit annoying to get to. You can configure metrics-addr
to expose them over TCP instead.

```
sudo curl --unix-socket /var/run/docker/metrics.sock http://localhost/metrics
```


# Adding monitoring

```
ssh docker-02 -lubuntu bash -c '"sudo apt-get install prometheus-node-exporter && sudo systemctl enable prometheus-node-exporter && sudo systemctl start prometheus-node-exporter && curl http://localhost:9100/metrics"'
```


