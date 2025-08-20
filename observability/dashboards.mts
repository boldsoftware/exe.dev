// This script creates a Grafana dashboard.
//
// NOTE: the prometheus.yml file is in bold.git/ops - not in this same repo.
//
// To get a GRAFANA_BEARER_TOKEN, visit
// https://grafana.crocodile-vector.ts.net/org/serviceaccounts
// GRAFANA_URL=https://grafana.crocodile-vector.ts.net/
//
// Run with
//   ./node_modules/.bin/tsx dashboards.mts
//

import { fileURLToPath } from "node:url";
import {
  DashboardBuilder,
  DashboardCursorSync,
  QueryVariableBuilder,
  RowBuilder,
} from "@grafana/grafana-foundation-sdk/dashboard";
import { TextMode, PanelBuilder as TextPanelBuilder } from "@grafana/grafana-foundation-sdk/text";
import { DataqueryBuilder } from "@grafana/grafana-foundation-sdk/prometheus";
import { PanelBuilder as TimeseriesBuilder } from "@grafana/grafana-foundation-sdk/timeseries";
import { PanelBuilder as StatBuilder } from "@grafana/grafana-foundation-sdk/stat";
import {
  BigValueColorMode,
  BigValueGraphMode,
  BigValueTextMode,
  GraphThresholdsStyleMode,
} from "@grafana/grafana-foundation-sdk/common";
import {
  ThresholdsConfig,
  ThresholdsMode,
  Threshold,
} from "@grafana/grafana-foundation-sdk/dashboard";
import { ThresholdsConfigBuilder } from "@grafana/grafana-foundation-sdk/dashboard";
import { GraphThresholdsStyleConfigBuilder } from "@grafana/grafana-foundation-sdk/common";
import {
  RuleBuilder,
  QueryBuilder as AlertQueryBuilder,
  Query as AlertQuery,
} from "@grafana/grafana-foundation-sdk/alerting";
const TOKEN = process.env.GRAFANA_BEARER_TOKEN;
const GRAFANA_URL = process.env.GRAFANA_URL;

// Interface for alert configuration
interface AlertConfig {
  threshold: number;
  condition: "gt" | "lt" | "eq" | "ne"; // greater than, less than, equal, not equal
  forDuration?: string; // e.g., "5m", "10s"
  summary?: string;
  description?: string;
}

// Storage for alerts to be created
const alertsToCreate: Array<{
  panelTitle: string;
  query: string;
  alertConfig: AlertConfig;
  dashboardUID: string;
}> = [];

// Cache for the default Prometheus datasource UID
let defaultPrometheusDatasourceUID: string | null = null;

// TODO: sections for sshpiper, once that's up and running.
function makeDevExeDashboard() {
  // Declare the name and define a unique id.
  const dash = new DashboardBuilder("exe.dev Dashboard");
  dash
    .uid("exe-dev-dashboard")
    .tags(["generated", "exe.dev"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  // Helper function for adding charts.
  const addTimeseriesChart = makeAddTimeseriesChart(dash, "exe-dev-dashboard");

  // README panel for auto-generated dashboard
  dash.withPanel(
    new TextPanelBuilder()
      .title("README - Auto Generated Dashboard")
      .content(
        `⚠️ **This dashboard is automatically generated** ⚠️\n\n` +
          `Do not edit this dashboard manually! All changes will be overwritten.\n\n` +
          `To modify this dashboard:\n` +
          `1. Edit the code in \`observability/dashboards.mts\`\n` +
          `2. Run \`./node_modules/.bin/tsx dashboards.mts\` to update\n\n` +
          `Last updated: ${new Date().toISOString()} by ${import.meta.url}`,
      )
      .mode(TextMode.Markdown)
      .gridPos({ x: 0, y: 0, w: 24, h: 4 }),
  );

  // Row 1: HTTP metrics overview (starting at y: 4 after README)
  addTimeseriesChart(
    "HTTP Requests Rate",
    `rate(promhttp_metric_handler_requests_total{job="exed"}[$__rate_interval])`,
    {
      panelCustomization: (x) => x.gridPos({ x: 0, y: 4, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart("HTTP Requests in Flight", `promhttp_metric_handler_requests_in_flight{job="exed"}`, {
    panelCustomization: (x) => x.min(0).gridPos({ x: 8, y: 4, w: 8, h: 6 }),
  });

  addTimeseriesChart(
    "HTTP Request Success Rate",
    `rate(promhttp_metric_handler_requests_total{job="exed",code="200"}[$__rate_interval]) / rate(promhttp_metric_handler_requests_total{job="exed"}[$__rate_interval]) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).max(100).gridPos({ x: 16, y: 4, w: 8, h: 6 }),
    },
  );

  // Row 2: SSH connections and activity
  addTimeseriesChart(
    "SSH Connections Rate",
    `rate(ssh_connections_total[5m])`,
    {
      panelCustomization: (x) => x.gridPos({ x: 0, y: 10, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart("Current SSH Connections", `ssh_connections_current`, {
    panelCustomization: (x) => x.min(0).gridPos({ x: 8, y: 10, w: 8, h: 6 }),
  });

  addTimeseriesChart(
    "SSH Auth Attempts Rate",
    `rate(ssh_auth_attempts_total[5m])`,
    {
      panelCustomization: (x) =>
        x.gridPos({ x: 16, y: 10, w: 8, h: 6 }),
    },
  );

  // Row 3: SSH session details
  addTimeseriesChart(
    "SSH Session Duration (95th percentile)",
    `histogram_quantile(0.95, rate(ssh_session_duration_seconds_bucket[5m]))`,
    {
      panelCustomization: (x) => x.unit("s").gridPos({ x: 0, y: 16, w: 12, h: 6 }),
    },
  );

  addTimeseriesChart(
    "HTTP Error Rate",
    `rate(promhttp_metric_handler_requests_total{job="exed",code=~"[45].."}[5m]) / rate(promhttp_metric_handler_requests_total{job="exed"}[5m]) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").gridPos({ x: 12, y: 16, w: 12, h: 6 }),
    },
  );

  // sshpiperd metrics
  addTimeseriesChart(
    "sshpiper Pipe Open Connections",
    `rate(sshpiper_pipe_open_connections[5m])`,
    {
      panelCustomization: (x) => x.gridPos({ x: 0, y: 10, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart(
    "sshpiper Pipe Create Errors",
    `rate(sshpiper_pipe_create_errors[5m])`,
    {
      panelCustomization: (x) => x.gridPos({ x: 8, y: 10, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart(
    "sshpiper Upstream Auth Failures",
    `rate(sshpiper_upstream_auth_failures[5m])`,
    {
      panelCustomization: (x) => x.gridPos({ x: 16, y: 10, w: 8, h: 6 }),
    },
  );


  // Row 6: SQLite Connection Pool Metrics
  dash.withRow(new RowBuilder("SQLite Connection Pool").gridPos({ x: 0, y: 34, w: 24, h: 1 }));



  // SQL-level connection metrics
  const sqlPoolPanel = new TimeseriesBuilder()
    .title("SQL Connection Pool")
    .min(0)
    .gridPos({ x: 0, y: 35, w: 8, h: 6 })
    .withTarget(
      new DataqueryBuilder().expr("sqlite_pool_open_connections{job=\"exed\"}").legendFormat("Open Connections"),
    )
    .withTarget(
      new DataqueryBuilder().expr("sqlite_pool_in_use_connections{job=\"exed\"}").legendFormat("In Use"),
    )
    .withTarget(new DataqueryBuilder().expr("sqlite_pool_idle_connections{job=\"exed\"}").legendFormat("Idle"));
  dash.withPanel(sqlPoolPanel);

  // Writer connections
  const writerPoolPanel = new TimeseriesBuilder()
    .title("Writer Connections")
    .min(0)
    .gridPos({ x: 8, y: 35, w: 8, h: 6 })
    .withTarget(
      new DataqueryBuilder().expr("sqlite_pool_available_writers{job=\"exed\"}").legendFormat("Available"),
    )
    .withTarget(new DataqueryBuilder().expr("sqlite_pool_total_writers{job=\"exed\"}").legendFormat("Total"));
  dash.withPanel(writerPoolPanel);

  // Reader connections
  const readerPoolPanel = new TimeseriesBuilder()
    .title("Reader Connections")
    .min(0)
    .gridPos({ x: 16, y: 35, w: 8, h: 6 })
    .withTarget(
      new DataqueryBuilder().expr("sqlite_pool_available_readers{job=\"exed\"}").legendFormat("Available"),
    )
    .withTarget(new DataqueryBuilder().expr("sqlite_pool_total_readers{job=\"exed\"}").legendFormat("Total"));
  dash.withPanel(readerPoolPanel);

  // Row 7: SQLite Transaction Metrics
  dash.withRow(new RowBuilder("SQLite Transaction Metrics").gridPos({ x: 0, y: 41, w: 24, h: 1 }));

  addTimeseriesChart("SQLite Transaction Leaks", `rate(sqlite_tx_leaks_total{job="exed"}[5m])`, {
    panelCustomization: (x) => x.gridPos({ x: 0, y: 42, w: 8, h: 6 }),
  });

  addTimeseriesChart("SQLite Read Transaction Leaks", `rate(sqlite_rx_leaks_total{job="exed"}[5m])`, {
    panelCustomization: (x) => x.gridPos({ x: 8, y: 42, w: 8, h: 6 }),
  });

  addTimeseriesChart(
    "SQLite Transaction Latency (95th percentile)",
    `histogram_quantile(0.95, rate(sqlite_tx_latency_bucket{job="exed"}[5m])) / 1000`,
    {
      panelCustomization: (x) => x.unit("ms").gridPos({ x: 16, y: 42, w: 8, h: 6 }),
    },
  );

  return dash;
}

function makeContainerMetricsDashboard() {
  // Declare the name and define a unique id.
  const dash = new DashboardBuilder("exe.dev Container Metrics Dashboard");
  dash
    .uid("exe-dev-container-metrics-dashboard")
    .tags(["generated", "containers", "cadvisor"])
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  // Helper function for adding charts.
  const addTimeseriesChart = makeAddTimeseriesChart(dash, "exe-dev-container-metrics-dashboard");

  // Variable definitions for filtering
  dash.withVariable(
    new QueryVariableBuilder("image")
      .includeAll(true)
      .query('label_values(container_last_seen{container_label_managed_by="exe"}, image)')
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1),
  );

  dash.withVariable(
    new QueryVariableBuilder("instance")
      .includeAll(true)
      .query('label_values(container_last_seen{container_label_managed_by="exe"}, instance)')
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1),
  );

  dash.withVariable(
    new QueryVariableBuilder("user_id")
      .includeAll(true)
      .query(
        'label_values(container_last_seen{container_label_managed_by="exe"}, container_label_user_id)',
      )
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1),
  );

  dash.withVariable(
    new QueryVariableBuilder("team")
      .includeAll(true)
      .query(
        'label_values(container_last_seen{container_label_managed_by="exe"}, container_label_team)',
      )
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1),
  );

  // Filter for containers with sketch="true" label and selected variables
  const CONTAINER_FILTER =
    'container_label_managed_by="exe",image=~"$image",instance=~"$instance",container_label_user_id=~"$user_id",container_label_team=~"$team"';

  // README panel for auto-generated dashboard
  dash.withPanel(
    new TextPanelBuilder()
      .title("README - Auto Generated Dashboard")
      .content(
        `⚠️ **This dashboard is automatically generated** ⚠️\n\n` +
          `Do not edit this dashboard manually! All changes will be overwritten.\n\n` +
          `To modify this dashboard:\n` +
          `1. Edit the code in \`observability/dashboards.mts\`\n` +
          `2. Run \`./node_modules/.bin/tsx dashboards.mts\` to update\n\n` +
          `Last updated: ${new Date().toISOString()} by ${import.meta.url}`,
      )
      .mode(TextMode.Markdown)
      .gridPos({ x: 0, y: 0, w: 24, h: 4 }),
  );

  // Row 1: Container Overview (starting at y: 4 after README)
  dash.withRow(new RowBuilder("Container Overview").gridPos({ x: 0, y: 4, w: 24, h: 1 }));

  // Container count
  const containerCountPanel = new StatBuilder()
    .title("Running Containers")
    .gridPos({ x: 0, y: 5, w: 6, h: 4 })
    .withTarget(
      new DataqueryBuilder()
        .expr(`count(container_last_seen{${CONTAINER_FILTER}})`)
        .legendFormat("Containers"),
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(containerCountPanel);

  // Average container age
  const avgContainerAgePanel = new StatBuilder()
    .title("Average Container Age")
    .gridPos({ x: 6, y: 5, w: 6, h: 4 })
    .withTarget(
      new DataqueryBuilder()
        .expr(`avg(time() - container_start_time_seconds{${CONTAINER_FILTER}})`)
        .legendFormat("Age"),
    )
    .unit("s")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(avgContainerAgePanel);

  // Total CPU usage across all containers
  const totalCpuPanel = new StatBuilder()
    .title("Total CPU Usage")
    .gridPos({ x: 12, y: 5, w: 6, h: 4 })
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(container_cpu_usage_seconds_total{${CONTAINER_FILTER}}[5m])) * 100`)
        .legendFormat("CPU %"),
    )
    .unit("percent")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalCpuPanel);

  // Total memory usage across all containers
  const totalMemoryPanel = new StatBuilder()
    .title("Total Memory Usage")
    .gridPos({ x: 18, y: 5, w: 6, h: 4 })
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(container_memory_working_set_bytes{${CONTAINER_FILTER}})`)
        .legendFormat("Memory"),
    )
    .unit("bytes")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalMemoryPanel);

  // Row 2: CPU Metrics (starting at y: 9)
  dash.withRow(new RowBuilder("CPU Metrics").gridPos({ x: 0, y: 9, w: 24, h: 1 }));

  addTimeseriesChart(
    "CPU Usage % per Container",
    `rate(container_cpu_usage_seconds_total{${CONTAINER_FILTER}}[5m]) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).gridPos({ x: 0, y: 10, w: 12, h: 6 }),
    },
  );

  addTimeseriesChart(
    "CPU Throttling per Container",
    `rate(container_cpu_cfs_throttled_seconds_total{${CONTAINER_FILTER}}[5m])`,
    {
      panelCustomization: (x) => x.unit("s").min(0).gridPos({ x: 12, y: 10, w: 12, h: 6 }),
    },
  );

  // Row 3: Memory Metrics (starting at y: 16)
  dash.withRow(new RowBuilder("Memory Metrics").gridPos({ x: 0, y: 16, w: 24, h: 1 }));

  addTimeseriesChart(
    "Memory Usage per Container",
    `container_memory_working_set_bytes{${CONTAINER_FILTER}}`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0).gridPos({ x: 0, y: 17, w: 12, h: 6 }),
    },
  );

  addTimeseriesChart(
    "Memory Usage % per Container",
    `(container_memory_working_set_bytes{${CONTAINER_FILTER}} / container_spec_memory_limit_bytes{${CONTAINER_FILTER}}) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100).gridPos({ x: 12, y: 17, w: 12, h: 6 }),
    },
  );

  // Row 4: Container Lifecycle (starting at y: 23)
  dash.withRow(new RowBuilder("Container Lifecycle").gridPos({ x: 0, y: 23, w: 24, h: 1 }));

  addTimeseriesChart(
    "Container Age (Uptime)",
    `time() - container_start_time_seconds{${CONTAINER_FILTER}}`,
    {
      panelCustomization: (x) => x.unit("s").min(0).gridPos({ x: 0, y: 24, w: 12, h: 6 }),
    },
  );

  addTimeseriesChart("Container Restart Count", `container_restart_count{${CONTAINER_FILTER}}`, {
    panelCustomization: (x) => x.min(0).gridPos({ x: 12, y: 24, w: 12, h: 6 }),
  });

  // Row 5: Network and File System (starting at y: 30)
  dash.withRow(new RowBuilder("Network & File System").gridPos({ x: 0, y: 30, w: 24, h: 1 }));

  addTimeseriesChart(
    "Network Receive Rate",
    `rate(container_network_receive_bytes_total{${CONTAINER_FILTER}}[5m])`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0).gridPos({ x: 0, y: 31, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart(
    "Network Transmit Rate",
    `rate(container_network_transmit_bytes_total{${CONTAINER_FILTER}}[5m])`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0).gridPos({ x: 8, y: 31, w: 8, h: 6 }),
    },
  );

  addTimeseriesChart("File System Usage", `container_fs_usage_bytes{${CONTAINER_FILTER}}`, {
    panelCustomization: (x) => x.unit("bytes").min(0).gridPos({ x: 16, y: 31, w: 8, h: 6 }),
  });

  return dash;
}

// Helper method to create "addTimeseriesChart" methods for your dashboard.
function makeAddTimeseriesChart(dash: DashboardBuilder, dashboardUID: string) {
  const builders = {
    buildPanel: () => new TimeseriesBuilder().gridPos({ x: 0, y: 0, w: 8, h: 6 }),
    // You might need to specify a default datasource, like so:
    // new DataqueryBuilder().datasource({ type: "prometheus", uid: "grafanacloud-prom" })
    buildQueryTarget: () => new DataqueryBuilder(),
  };
  return makeAddChart<TimeseriesBuilder>(dash, builders, dashboardUID);
}
/**
 * Creates alerts using the Grafana alerting API
 */
async function createAlerts() {
  if (alertsToCreate.length === 0) {
    console.log("No alerts to create.");
    return;
  }

  console.log(`Creating ${alertsToCreate.length} alerts...`);

  // Ensure the folder exists
  await ensureAlertFolder();

  // Get the default Prometheus datasource UID
  const prometheusUID = await getDefaultPrometheusDatasourceUID();

  for (const alertSpec of alertsToCreate) {
    try {
      const alertUID = `alert-${alertSpec.panelTitle.toLowerCase().replace(/[^a-z0-9]/g, "-")}`;
      const alertTitle = `${alertSpec.panelTitle} Alert`;

      // Check if alert already exists and delete it to allow updates
      const existingAlertResponse = await fetch(
        `${GRAFANA_URL}api/v1/provisioning/alert-rules/${alertUID}`,
        {
          headers: {
            Authorization: `Bearer ${TOKEN}`,
          },
        },
      );

      if (existingAlertResponse.ok) {
        console.log(`🔄 Updating existing alert for ${alertSpec.panelTitle}...`);
        await fetch(`${GRAFANA_URL}api/v1/provisioning/alert-rules/${alertUID}`, {
          method: "DELETE",
          headers: {
            Authorization: `Bearer ${TOKEN}`,
          },
        });
      }

      const alertRule = {
        uid: alertUID,
        title: alertTitle,
        condition: "C",
        data: [
          {
            refId: "A",
            queryType: "",
            relativeTimeRange: {
              from: 300,
              to: 0,
            },
            datasourceUid: prometheusUID,
            model: {
              expr: convertQueryForAlert(alertSpec.query),
              interval: "",
              refId: "A",
            },
          },
          {
            refId: "B",
            queryType: "",
            relativeTimeRange: {
              from: 0,
              to: 0,
            },
            datasourceUid: "__expr__",
            model: {
              datasource: {
                type: "__expr__",
                uid: "__expr__",
              },
              expression: "A",
              reducer: "last",
              intervalMs: 1000,
              maxDataPoints: 43200,
              refId: "B",
              type: "reduce",
            },
          },
          {
            refId: "C",
            queryType: "",
            relativeTimeRange: {
              from: 0,
              to: 0,
            },
            datasourceUid: "__expr__",
            model: {
              datasource: {
                type: "__expr__",
                uid: "__expr__",
              },
              expression: `$B ${alertSpec.alertConfig.condition === "gt" ? ">" : alertSpec.alertConfig.condition === "lt" ? "<" : alertSpec.alertConfig.condition === "eq" ? "==" : "!="} ${alertSpec.alertConfig.threshold}`,
              intervalMs: 1000,
              maxDataPoints: 43200,
              refId: "C",
              type: "math",
            },
          },
        ],
        intervalSeconds: 60,
        noDataState: "NoData",
        execErrState: "Alerting",
        for: alertSpec.alertConfig.forDuration || "1m",
        ruleGroup: "dashboard-alerts",
        annotations: {
          summary:
            alertSpec.alertConfig.summary || `${alertSpec.panelTitle} has exceeded threshold`,
          description:
            alertSpec.alertConfig.description ||
            `${alertSpec.panelTitle} is above ${alertSpec.alertConfig.threshold}`,
        },
        labels: {
          panel: alertSpec.panelTitle,
          dashboard: alertSpec.dashboardUID,
        },
        folderUID: "auto-alerts",
      };

      const response = await fetch(`${GRAFANA_URL}api/v1/provisioning/alert-rules`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${TOKEN}`,
        },
        body: JSON.stringify(alertRule),
      });

      if (response.ok) {
        console.log(`✓ Created alert for ${alertSpec.panelTitle}`);
      } else {
        const errorText = await response.text();
        console.error(
          `✗ Failed to create alert for ${alertSpec.panelTitle}: ${response.status} - ${errorText}`,
        );
      }
    } catch (error) {
      console.error(`✗ Error creating alert for ${alertSpec.panelTitle}:`, error);
    }
  }
}

/**
 * Ensures the alert folder exists
 */
/**
 * Converts dashboard queries with variables to alert queries without variables
 */
function convertQueryForAlert(query: string): string {
  // Replace dashboard variables with patterns that work in alerts
  return query.replace(/instance=~"\$instance"/g, 'instance=~".+"');
}

/**
 * Gets the default Prometheus datasource UID
 */
async function getDefaultPrometheusDatasourceUID(): Promise<string> {
  if (defaultPrometheusDatasourceUID) {
    return defaultPrometheusDatasourceUID;
  }

  try {
    const response = await fetch(`${GRAFANA_URL}api/datasources`, {
      headers: {
        Authorization: `Bearer ${TOKEN}`,
      },
    });

    if (response.ok) {
      const datasources = await response.json();
      const defaultPrometheus = datasources.find(
        (ds: any) => ds.type === "prometheus" && ds.isDefault,
      );

      if (defaultPrometheus) {
        defaultPrometheusDatasourceUID = defaultPrometheus.uid;
        return defaultPrometheus.uid;
      } else {
        // Fallback to first Prometheus datasource if no default
        const firstPrometheus = datasources.find((ds: any) => ds.type === "prometheus");
        if (firstPrometheus) {
          defaultPrometheusDatasourceUID = firstPrometheus.uid;
          return firstPrometheus.uid;
        }
      }
    }
  } catch (error) {
    console.error("✗ Error getting Prometheus datasource:", error);
  }

  throw new Error("No Prometheus datasource found");
}

async function ensureAlertFolder() {
  try {
    const response = await fetch(`${GRAFANA_URL}api/folders/auto-alerts`, {
      headers: {
        Authorization: `Bearer ${TOKEN}`,
      },
    });

    if (response.status === 404) {
      // Folder doesn't exist, create it
      const createResponse = await fetch(`${GRAFANA_URL}api/folders`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${TOKEN}`,
        },
        body: JSON.stringify({
          title: "Auto-Generated Alerts",
          uid: "auto-alerts",
        }),
      });

      if (createResponse.ok) {
        console.log("✓ Created alerts folder");
      } else {
        console.error("✗ Failed to create alerts folder:", await createResponse.text());
      }
    }
  } catch (error) {
    console.error("✗ Error ensuring alert folder:", error);
  }
}

/**
 * Invokes the Grafana API to create or update the given dashboard.
 */
async function createDashboard(dash: DashboardBuilder) {
  let version = null;
  const built = dash.build();
  try {
    // 1) Try to fetch existing dashboard to get its version:
    const getResponse = await fetch(`${GRAFANA_URL}api/dashboards/uid/${built.uid}`, {
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${TOKEN}`,
      },
    });
    // If response is OK, parse JSON and retrieve version:
    if (getResponse.ok) {
      const getData = await getResponse.json();
      version = getData.dashboard.version;
      built.version = version!;
    } else if (getResponse.status === 404) {
      // If the dashboard does not exist, set version to 0
      built.version = 0;
    } else {
      // Other non-200 responses are treated as errors:
      throw new Error(`Fetch GET failed with status: ${getResponse.status}`);
    }
    // 2) POST (create or overwrite) the dashboard:
    try {
      const postResponse = await fetch(`${GRAFANA_URL}api/dashboards/db`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${TOKEN}`,
        },
        body: JSON.stringify({
          dashboard: built,
          overwite: true,
          message: "Automated update",
        }),
      });
      if (!postResponse.ok) {
        throw new Error(`Fetch POST failed with status:
${postResponse.status}`);
      }
      // Parse JSON for the returned info
      const data = await postResponse.json();
      // Fix double slash issue by removing trailing slash from GRAFANA_URL if present
      const baseUrl = GRAFANA_URL?.endsWith("/") ? GRAFANA_URL.slice(0, -1) : GRAFANA_URL;
      console.log("Dashboard updated. Dashboard URL:", `${baseUrl}${data.url}`);
    } catch (error) {
      console.error("Error posting dashboard:", error);
      throw error;
    }
  } catch (error) {
    console.error("Error creating dashboard:", error);
  }
}

// Helper method for the helper methods. This facilitates using panel types.
function makeAddChart<T extends TimeseriesBuilder>(
  dash: DashboardBuilder,
  builders: { buildPanel: () => T; buildQueryTarget: () => DataqueryBuilder },
  dashboardUID: string,
) {
  return function addChart(
    title: string,
    query: string,
    {
      panelCustomization,
      queryCustomization,
      alert,
    }: {
      panelCustomization?: (panel: T) => T;
      queryCustomization?: (dataQuery: DataqueryBuilder) => DataqueryBuilder;
      alert?: AlertConfig;
    } = {},
  ) {
    const panel = builders.buildPanel().title(title);
    const queryTarget = builders.buildQueryTarget().expr(query);

    // Apply query customization if provided
    if (queryCustomization) {
      queryCustomization(queryTarget);
    }

    // Configure thresholds and alert if provided
    if (alert) {
      // Add threshold visualization to the chart
      const thresholds = new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: alert.threshold, color: "red" } as Threshold,
        ]);

      const thresholdStyle = new GraphThresholdsStyleConfigBuilder().mode(
        GraphThresholdsStyleMode.Dashed,
      );

      panel.thresholds(thresholds).thresholdsStyle(thresholdStyle);

      // Store alert configuration for later creation
      alertsToCreate.push({
        panelTitle: title,
        query: query,
        alertConfig: alert,
        dashboardUID: dashboardUID,
      });
    }

    // Apply panel customization if provided (after threshold configuration)
    if (panelCustomization) {
      panelCustomization(panel);
    }

    // Attach the query target to the panel
    panel.withTarget(queryTarget);
    dash.withPanel(panel);
  };
}

async function main() {
  if (TOKEN === undefined) {
    console.error(
      "Please provide a Grafana bearer token in the GRAFANA_BEARER_TOKEN environment variable.",
    );
    process.exit(1);
  }
  if (GRAFANA_URL === undefined) {
    console.error("Please provide the Grafana URL in the GRAFANA_URL environment variable.");
    process.exit(1);
  }
  await createDashboard(makeDevExeDashboard());
  await createDashboard(makeContainerMetricsDashboard());

  // Create alerts after dashboards are created
  await createAlerts();
}
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main();
}
