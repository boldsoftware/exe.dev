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
import { execSync } from "node:child_process";
import {
  DashboardBuilder,
  DashboardCursorSync,
  QueryVariableBuilder,
  RowBuilder,
} from "@grafana/grafana-foundation-sdk/dashboard";
import {
  TextMode,
  PanelBuilder as TextPanelBuilder,
} from "@grafana/grafana-foundation-sdk/text";
import { DataqueryBuilder, PromQueryFormat } from "@grafana/grafana-foundation-sdk/prometheus";
import { PanelBuilder as TimeseriesBuilder } from "@grafana/grafana-foundation-sdk/timeseries";
import { PanelBuilder as StatBuilder } from "@grafana/grafana-foundation-sdk/stat";
import { PanelBuilder as TableBuilder } from "@grafana/grafana-foundation-sdk/table";
import {
  BigValueColorMode,
  BigValueGraphMode,
  BigValueTextMode,
  GraphThresholdsStyleMode,
  ScaleDistribution,
  StackingMode,
} from "@grafana/grafana-foundation-sdk/common";
import { ScaleDistributionConfigBuilder, StackingConfigBuilder } from "@grafana/grafana-foundation-sdk/common";
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

// Global layout state for panel positioning on a 24-unit wide grid.
// Panels flow left to right, wrapping to the next row when they exceed 24 units.
let layoutX = 0;
let layoutY = 0;
let layoutRowHeight = 0;

// Reset layout at the start of each dashboard
function resetLayout() {
  layoutX = 0;
  layoutY = 0;
  layoutRowHeight = 0;
}

// Returns gridPos with computed x/y and advances the position.
const gp = (pos: { w: number; h: number }): { x: number; y: number; w: number; h: number } => {
  // Wrap to next row if panel doesn't fit
  if (layoutX + pos.w > 24) {
    layoutX = 0;
    layoutY += layoutRowHeight;
    layoutRowHeight = 0;
  }
  const result = { x: layoutX, y: layoutY, w: pos.w, h: pos.h };
  layoutX += pos.w;
  layoutRowHeight = Math.max(layoutRowHeight, pos.h);
  return result;
};

const TOKEN = process.env.GRAFANA_BEARER_TOKEN;
const GRAFANA_URL = process.env.GRAFANA_URL;

// Interface for alert configuration
interface AlertConfig {
  threshold: number;
  condition: "gt" | "lt" | "eq" | "ne"; // greater than, less than, equal, not equal
  forDuration?: string; // e.g., "5m", "10s"
  noDataState?: "NoData" | "Alerting" | "OK"; // default: NoData
  summary?: string;
  description?: string;
  labels?: Record<string, string>; // additional labels for notification routing
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

// GitHub URL for the README link
const GITHUB_README_URL = "https://github.com/boldsoftware/exe/blob/main/observability/README.md";

// Compact README content for all dashboards
const README_CONTENT = `⚠️ Auto-generated dashboard - [edit in GitHub](${GITHUB_README_URL})`;

// Helper to add the stage variable to dashboards
function addStageVariable(dash: DashboardBuilder) {
  dash.withVariable(
    new QueryVariableBuilder("stage")
      .label("Stage")
      .includeAll(true)
      .query('label_values(up, stage)')
      .current({ text: "production", value: "production" })
      .multi(true)
      .sort(1)
  );
}

// exe.dev VMs Dashboard - VM-level metrics from exelet
function makeExeDevVMsDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("exe.dev VMs");
  dash
    .uid("exe-dev-vms-dashboard")
    .tags(["generated", "exe.dev"])
    .refresh("30s")
    .time({ from: "now-1h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  addStageVariable(dash);

  const addTimeseriesChart = makeAddTimeseriesChart(dash, "exe-dev-vms-dashboard");
  const STAGE_FILTER = 'stage=~"$stage"';

  // README panel
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Row 1: Overview Stats
  dash.withRow(new RowBuilder("Overview"));

  const runningVMsPanel = new StatBuilder()
    .title("Running VMs")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`count(exelet_vm_cpu_seconds_total{${STAGE_FILTER}})`)
        .legendFormat("VMs")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(runningVMsPanel);

  const totalCpuPanel = new StatBuilder()
    .title("Total CPU Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(exelet_vm_cpu_seconds_total{${STAGE_FILTER}}[5m]))`)
        .legendFormat("Cores")
    )
    .unit("short")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalCpuPanel);

  const totalMemoryPanel = new StatBuilder()
    .title("Total Memory Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(exelet_vm_memory_bytes{${STAGE_FILTER}})`)
        .legendFormat("Memory")
    )
    .unit("bytes")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalMemoryPanel);

  const totalDiskPanel = new StatBuilder()
    .title("Total Disk Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(exelet_vm_disk_used_bytes{${STAGE_FILTER}})`)
        .legendFormat("Disk")
    )
    .unit("bytes")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalDiskPanel);

  // Running VMs per Host chart
  addTimeseriesChart(
    "Running VMs per Host",
    `count by (instance) (exelet_vm_cpu_seconds_total{${STAGE_FILTER}})`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 24, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  // Row 2: CPU Usage Over Time
  dash.withRow(new RowBuilder("CPU Usage"));

  addTimeseriesChart(
    "Total VM CPU Usage (cores)",
    `sum(rate(exelet_vm_cpu_seconds_total{${STAGE_FILTER}}[$__rate_interval]))`,
    {
      panelCustomization: (x) => x.unit("short").min(0),
      gridPos: { w: 12, h: 8 },
    }
  );

  addTimeseriesChart(
    "CPU Usage per VM",
    `rate(exelet_vm_cpu_seconds_total{${STAGE_FILTER}}[$__rate_interval])`,
    {
      panelCustomization: (x) => x.unit("short").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{vm_name}}"),
    }
  );

  // Row 3: Memory Usage
  dash.withRow(new RowBuilder("Memory Usage"));

  addTimeseriesChart(
    "Total VM Memory Usage",
    `sum(exelet_vm_memory_bytes{${STAGE_FILTER}})`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 8 },
    }
  );

  addTimeseriesChart(
    "Memory Usage per VM",
    `exelet_vm_memory_bytes{${STAGE_FILTER}}`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{vm_name}}"),
    }
  );

  // Row 4: Disk Usage
  dash.withRow(new RowBuilder("Disk Usage"));

  addTimeseriesChart(
    "Total VM Disk Usage",
    `sum(exelet_vm_disk_used_bytes{${STAGE_FILTER}})`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 8 },
    }
  );

  addTimeseriesChart(
    "Disk Usage per VM",
    `exelet_vm_disk_used_bytes{${STAGE_FILTER}}`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{vm_name}}"),
    }
  );

  // Row 5: Network I/O
  dash.withRow(new RowBuilder("Network I/O"));

  addTimeseriesChart(
    "Total Network RX Rate",
    `sum(rate(exelet_vm_net_rx_bytes_total{${STAGE_FILTER}}[$__rate_interval]))`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0),
      gridPos: { w: 12, h: 8 },
    }
  );

  addTimeseriesChart(
    "Total Network TX Rate",
    `sum(rate(exelet_vm_net_tx_bytes_total{${STAGE_FILTER}}[$__rate_interval]))`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0),
      gridPos: { w: 12, h: 8 },
    }
  );

  addTimeseriesChart(
    "Network RX per VM",
    `rate(exelet_vm_net_rx_bytes_total{${STAGE_FILTER}}[$__rate_interval])`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{vm_name}}"),
    }
  );

  addTimeseriesChart(
    "Network TX per VM",
    `rate(exelet_vm_net_tx_bytes_total{${STAGE_FILTER}}[$__rate_interval])`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{vm_name}}"),
    }
  );

  // Row 6: Top Consumers
  dash.withRow(new RowBuilder("Top Consumers"));

  const topCpuTable = new TableBuilder()
    .title("Top 10 CPU Consumers (5m avg)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(
          `topk(10, rate(exelet_vm_cpu_seconds_total{${STAGE_FILTER}}[5m]))`
        )
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topCpuTable);

  const topMemoryTable = new TableBuilder()
    .title("Top 10 Memory Consumers")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, exelet_vm_memory_bytes{${STAGE_FILTER}})`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topMemoryTable);

  const topDiskTable = new TableBuilder()
    .title("Top 10 Disk Consumers")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, exelet_vm_disk_used_bytes{${STAGE_FILTER}})`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topDiskTable);

  const topNetworkTable = new TableBuilder()
    .title("Top 10 Network Consumers (5m avg)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(
          `topk(10, rate(exelet_vm_net_rx_bytes_total{${STAGE_FILTER}}[5m]) + rate(exelet_vm_net_tx_bytes_total{${STAGE_FILTER}}[5m]))`
        )
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topNetworkTable);

  return dash;
}

// Parse proto files to extract RPC method names
function getGrpcMethodsFromProtos(): string[] {
  const repoRoot = execSync("git rev-parse --show-toplevel", { encoding: "utf-8" }).trim();
  const output = execSync(`git grep "rpc.*returns" -- "*.proto"`, {
    cwd: repoRoot,
    encoding: "utf-8",
  });

  const methods: string[] = [];
  for (const line of output.split("\n")) {
    // Match: rpc MethodName(Request) returns ...
    const match = line.match(/rpc\s+(\w+)\s*\(/);
    if (match && !line.includes("deps/")) {
      // Skip deps/ (sshpiper protos)
      methods.push(match[1]);
    }
  }
  return methods;
}

// gRPC Metrics Dashboard - covers both client (exed) and server (exelet) metrics
function makeGrpcMetricsDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("gRPC Metrics");
  dash
    .uid("grpc-metrics-dashboard")
    .tags(["generated", "grpc"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  addStageVariable(dash);

  const STAGE_FILTER = 'stage=~"$stage"';

  // README panel
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Get RPC methods from proto files
  const grpcMethods = getGrpcMethodsFromProtos();

  // ========== SERVER METRICS SECTION ==========
  dash.withRow(new RowBuilder("Server Metrics"));

  // One row per gRPC method - server side
  for (const method of grpcMethods) {
    const methodFilter = `grpc_method="${method}",${STAGE_FILTER}`;

    // Request rate
    const ratePanel = new TimeseriesBuilder()
      .title(`${method} - Requests`)
      .gridPos(gp({ w: 8, h: 5 }))
      .min(0)
      .withTarget(
        new DataqueryBuilder()
          .expr(`sum(rate(grpc_server_handled_total{${methodFilter}}[$__rate_interval]))`)
          .legendFormat("req/s")
      );
    dash.withPanel(ratePanel);

    // Error rate
    const errorPanel = new TimeseriesBuilder()
      .title(`${method} - Errors`)
      .gridPos(gp({ w: 8, h: 5 }))
      .min(0)
      .withTarget(
        new DataqueryBuilder()
          .expr(`sum(rate(grpc_server_handled_total{${methodFilter},grpc_code!="OK"}[$__rate_interval])) by (grpc_code)`)
          .legendFormat("{{grpc_code}}")
      );
    dash.withPanel(errorPanel);

    // Latency percentiles
    const latencyPanel = new TimeseriesBuilder()
      .title(`${method} - Latency`)
      .unit("s")
      .min(0)
      .gridPos(gp({ w: 8, h: 5 }))
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.5, sum(rate(grpc_server_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p50")
      )
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.9, sum(rate(grpc_server_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p90")
      )
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.99, sum(rate(grpc_server_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p99")
      );
    dash.withPanel(latencyPanel);
  }

  // ========== CLIENT METRICS SECTION ==========
  dash.withRow(new RowBuilder("Client Metrics"));

  // One row per gRPC method - client side
  for (const method of grpcMethods) {
    const methodFilter = `grpc_method="${method}",${STAGE_FILTER}`;

    // Request rate
    const ratePanel = new TimeseriesBuilder()
      .title(`${method} - Requests`)
      .gridPos(gp({ w: 8, h: 5 }))
      .min(0)
      .withTarget(
        new DataqueryBuilder()
          .expr(`sum(rate(grpc_client_handled_total{${methodFilter}}[$__rate_interval]))`)
          .legendFormat("req/s")
      );
    dash.withPanel(ratePanel);

    // Error rate
    const errorPanel = new TimeseriesBuilder()
      .title(`${method} - Errors`)
      .gridPos(gp({ w: 8, h: 5 }))
      .min(0)
      .withTarget(
        new DataqueryBuilder()
          .expr(`sum(rate(grpc_client_handled_total{${methodFilter},grpc_code!="OK"}[$__rate_interval])) by (grpc_code)`)
          .legendFormat("{{grpc_code}}")
      );
    dash.withPanel(errorPanel);

    // Latency percentiles
    const latencyPanel = new TimeseriesBuilder()
      .title(`${method} - Latency`)
      .unit("s")
      .min(0)
      .gridPos(gp({ w: 8, h: 5 }))
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.5, sum(rate(grpc_client_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p50")
      )
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.9, sum(rate(grpc_client_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p90")
      )
      .withTarget(
        new DataqueryBuilder()
          .expr(`histogram_quantile(0.99, sum(rate(grpc_client_handling_seconds_bucket{${methodFilter}}[$__rate_interval])) by (le))`)
          .legendFormat("p99")
      );
    dash.withPanel(latencyPanel);
  }

  return dash;
}

function makeDevExeDashboard() {
  resetLayout();
  // Declare the name and define a unique id.
  const dash = new DashboardBuilder("exe.dev Dashboard");
  dash
    .uid("exe-dev-dashboard")
    .tags(["generated", "exe.dev"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  // Add stage variable for filtering production vs staging
  addStageVariable(dash);

  // Helper function for adding charts.
  const addTimeseriesChart = makeAddTimeseriesChart(dash, "exe-dev-dashboard");

  // Stage filter to be used in queries
  const STAGE_FILTER = 'stage=~"$stage"';

  // README panel for auto-generated dashboard
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Filters for HTTP metrics
  const WEB_FILTER = `proxy="false",${STAGE_FILTER}`;
  const PROXY_FILTER = `proxy="true",${STAGE_FILTER}`;

  // ========== HTTP WEB SERVER METRICS ==========
  dash.withRow(
    new RowBuilder("HTTP - Web Server").gridPos(gp({ w: 24, h: 1 }))
  );

  // Row 1: Aggregate web server metrics
  addTimeseriesChart(
    "Web Request Rate",
    `sum(rate(http_requests_total{${WEB_FILTER}}[$__rate_interval])) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Web Requests In Flight",
    `sum(http_requests_in_flight{${WEB_FILTER}}) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Web Success Rate",
    `sum(rate(http_requests_total{${WEB_FILTER},code=~"2.."}[$__rate_interval])) by (stage) / sum(rate(http_requests_total{${WEB_FILTER}}[$__rate_interval])) by (stage) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  // Row 2: Web server by status code and path
  const webStatusCodePanel = new TimeseriesBuilder()
    .title("Web Requests by Status Code")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(http_requests_total{${WEB_FILTER}}[$__rate_interval])) by (code, stage)`)
        .legendFormat("{{stage}} {{code}}")
    );
  dash.withPanel(webStatusCodePanel);

  const webByPathPanel = new TimeseriesBuilder()
    .title("Web Request Rate by Path (Top 10)")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(rate(http_requests_total{${WEB_FILTER}}[$__rate_interval])) by (path, stage))`)
        .legendFormat("{{stage}} {{path}}")
    );
  dash.withPanel(webByPathPanel);

  // Top Request Paths Table
  const topRequestPathsTable = new TableBuilder()
    .title("Top Request Paths")
    .gridPos(gp({ w: 24, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(20, sum(rate(http_requests_total{${WEB_FILTER}}[5m])) by (path))`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topRequestPathsTable);

  // ========== HTTP WEB SERVER ERRORS ==========
  dash.withRow(
    new RowBuilder("HTTP - Web Server Errors").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Web 4xx Error Rate",
    `sum(rate(http_requests_total{${WEB_FILTER},code=~"4.."}[$__rate_interval])) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Web 5xx Error Rate",
    `sum(rate(http_requests_total{${WEB_FILTER},code=~"5.."}[$__rate_interval])) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Web Error Percentage",
    `sum(rate(http_requests_total{${WEB_FILTER},code=~"[45].."}[$__rate_interval])) by (stage) / sum(rate(http_requests_total{${WEB_FILTER}}[$__rate_interval])) by (stage) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  // Errors by path
  const web4xxByPathPanel = new TimeseriesBuilder()
    .title("Web 4xx Errors by Path")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(http_requests_total{${WEB_FILTER},code=~"4.."}[$__rate_interval])) by (path, code, stage)`)
        .legendFormat("{{stage}} {{path}} ({{code}})")
    );
  dash.withPanel(web4xxByPathPanel);

  const web5xxByPathPanel = new TimeseriesBuilder()
    .title("Web 5xx Errors by Path")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(http_requests_total{${WEB_FILTER},code=~"5.."}[$__rate_interval])) by (path, code, stage)`)
        .legendFormat("{{stage}} {{path}} ({{code}})")
    );
  dash.withPanel(web5xxByPathPanel);

  // ========== HTTP PROXY METRICS ==========
  dash.withRow(
    new RowBuilder("HTTP - Proxies").gridPos(gp({ w: 24, h: 1 }))
  );

  // Aggregate proxy metrics
  addTimeseriesChart(
    "Proxy Request Rate",
    `sum(rate(http_requests_total{${PROXY_FILTER}}[$__rate_interval])) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Proxy Requests In Flight",
    `sum(http_requests_in_flight{${PROXY_FILTER}}) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Proxy Success Rate",
    `sum(rate(http_requests_total{${PROXY_FILTER},code=~"2.."}[$__rate_interval])) by (stage) / sum(rate(http_requests_total{${PROXY_FILTER}}[$__rate_interval])) by (stage) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  // Proxy by status code and by box
  const proxyStatusCodePanel = new TimeseriesBuilder()
    .title("Proxy Requests by Status Code")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(http_requests_total{${PROXY_FILTER}}[$__rate_interval])) by (code, stage)`)
        .legendFormat("{{stage}} {{code}}")
    );
  dash.withPanel(proxyStatusCodePanel);

  const proxyByBoxPanel = new TimeseriesBuilder()
    .title("Proxy Request Rate by Box (Top 10)")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(rate(http_requests_total{${PROXY_FILTER}}[$__rate_interval])) by (box, stage))`)
        .legendFormat("{{stage}} {{box}}")
    );
  dash.withPanel(proxyByBoxPanel);

  // Proxy errors by box
  const proxyErrorsByBoxPanel = new TimeseriesBuilder()
    .title("Proxy Errors by Box")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(http_requests_total{${PROXY_FILTER},code=~"[45].."}[$__rate_interval])) by (box, stage)`)
        .legendFormat("{{stage}} {{box}}")
    );
  dash.withPanel(proxyErrorsByBoxPanel);

  const proxyInFlightByBoxPanel = new TimeseriesBuilder()
    .title("Proxy Requests In Flight by Box")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(http_requests_in_flight{${PROXY_FILTER}}) by (box, stage)`)
        .legendFormat("{{stage}} {{box}}")
    );
  dash.withPanel(proxyInFlightByBoxPanel);

  // ========== SSH METRICS ==========
  dash.withRow(
    new RowBuilder("SSH").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "SSH Connections Rate",
    `rate(ssh_connections_total{${STAGE_FILTER}}[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart("Current SSH Connections", `ssh_connections_current{${STAGE_FILTER}}`, {
    panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
  });

  addTimeseriesChart(
    "SSH Auth Attempts Rate",
    `rate(ssh_auth_attempts_total{${STAGE_FILTER}}[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "SSH Session Duration (95th percentile)",
    `histogram_quantile(0.95, rate(ssh_session_duration_seconds_bucket{${STAGE_FILTER}}[5m]))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 12, h: 6 },
    }
  );

  // exed uptime - logarithmic y-axis to see deployments and crashes
  const uptimePanel = new TimeseriesBuilder()
    .title("exed uptime")
    .unit("s")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .scaleDistribution(
      new ScaleDistributionConfigBuilder()
        .type(ScaleDistribution.Log)
        .log(10)
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`time() - process_start_time_seconds{job="exed",${STAGE_FILTER}}`)
        .legendFormat("{{instance}}")
    );
  dash.withPanel(uptimePanel);

  // exelet uptime - logarithmic y-axis to see deployments and crashes
  const exeletUptimePanel = new TimeseriesBuilder()
    .title("exelet uptime")
    .unit("s")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .scaleDistribution(
      new ScaleDistributionConfigBuilder()
        .type(ScaleDistribution.Log)
        .log(10)
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`time() - process_start_time_seconds{job="exelet",${STAGE_FILTER}}`)
        .legendFormat("{{instance}}")
    );
  dash.withPanel(exeletUptimePanel);

  // SQLite Connection Pool Metrics
  dash.withRow(
    new RowBuilder("SQLite Connection Pool").gridPos(gp({ w: 24, h: 1 }))
  );

  // SQL-level connection metrics
  const sqlPoolPanel = new TimeseriesBuilder()
    .title("SQL Connection Pool")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_open_connections{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Open Connections")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_in_use_connections{job="exed",${STAGE_FILTER}}`)
        .legendFormat("In Use")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_idle_connections{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Idle")
    );
  dash.withPanel(sqlPoolPanel);

  // Writer connections
  const writerPoolPanel = new TimeseriesBuilder()
    .title("Writer Connections")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_available_writers{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Available")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_total_writers{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Total")
    );
  dash.withPanel(writerPoolPanel);

  // Reader connections
  const readerPoolPanel = new TimeseriesBuilder()
    .title("Reader Connections")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_available_readers{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Available")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sqlite_pool_total_readers{job="exed",${STAGE_FILTER}}`)
        .legendFormat("Total")
    );
  dash.withPanel(readerPoolPanel);

  // SQLite Transaction Metrics
  dash.withRow(
    new RowBuilder("SQLite Transaction Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "SQLite Transaction Leaks",
    `increase(sqlite_tx_leaks_total{job="exed",${STAGE_FILTER}}[$__rate_interval])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "SQLite Read Transaction Leaks",
    `increase(sqlite_rx_leaks_total{job="exed",${STAGE_FILTER}}[$__rate_interval])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "SQLite Transaction Latency (95th percentile)",
    `histogram_quantile(0.95, rate(sqlite_tx_latency_bucket{job="exed",${STAGE_FILTER}}[5m])) / 1000`,
    {
      panelCustomization: (x) =>
        x.unit("ms"),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Box creation time (user-perceived)
  dash.withRow(
    new RowBuilder("Box Creation Time").gridPos(gp({ w: 24, h: 1 }))
  );

  // Box creation latency percentiles - all on one chart
  const boxCreationPanel = new TimeseriesBuilder()
    .title("Box Creation Latency")
    .unit("s")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.5, rate(box_creation_time_seconds_bucket{${STAGE_FILTER}}[$__rate_interval]))`)
        .legendFormat("p50")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.9, rate(box_creation_time_seconds_bucket{${STAGE_FILTER}}[$__rate_interval]))`)
        .legendFormat("p90")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.99, rate(box_creation_time_seconds_bucket{${STAGE_FILTER}}[$__rate_interval]))`)
        .legendFormat("p99")
    );
  dash.withPanel(boxCreationPanel);

  addTimeseriesChart(
    "Box Creation Rate",
    `rate(box_creation_time_seconds_count{${STAGE_FILTER}}[$__rate_interval])`,
    {
      gridPos: { w: 12, h: 6 },
    }
  );

  addTimeseriesChart(
    "CreateInstance Errors",
    `sum(rate(grpc_server_handled_total{grpc_method="CreateInstance",grpc_code!="OK",${STAGE_FILTER}}[$__rate_interval])) by (grpc_code)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{grpc_code}}"),
      alert: {
        threshold: 0,
        condition: "gt",
        forDuration: "1m",
        noDataState: "OK",
        summary: "CreateInstance is failing",
        description: "CreateInstance gRPC calls are returning errors",
      },
    }
  );

  addTimeseriesChart(
    "CreateInstance Success Rate",
    `sum(rate(grpc_server_handled_total{grpc_method="CreateInstance",grpc_code="OK",${STAGE_FILTER}}[$__rate_interval]))`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
    }
  );

  // Certificate issuance
  dash.withRow(
    new RowBuilder("Certificate Issuance").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Let's Encrypt Requests Rate",
    `rate(letsencrypt_cert_requests_total{${STAGE_FILTER}}[$__rate_interval])`,
    {
      panelCustomization: (x) =>
        x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Blog traffic
  dash.withRow(
    new RowBuilder("Blog").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Blog Hit Rate",
    `sum(rate(blog_page_hits_total{job="blogd",stage=~"$stage"}[$__rate_interval])) by (stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}}"),
    }
  );

  addTimeseriesChart(
    "Blog Hits by Path (Top 10)",
    `topk(10, sum(rate(blog_page_hits_total{job="blogd",stage=~"$stage"}[$__rate_interval])) by (path, stage))`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}} {{path}}"),
    }
  );

  // Entity Counts
  dash.withRow(
    new RowBuilder("Entity Counts").gridPos(gp({ w: 24, h: 1 }))
  );

  const loginUsersPanel = new StatBuilder()
    .title("Login Users")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`users_total{type="login",stage="production"}`)
        .legendFormat("Login Users")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(loginUsersPanel);

  const devUsersPanel = new StatBuilder()
    .title("Dev Users")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`users_total{type="dev",stage="production"}`)
        .legendFormat("Dev Users")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(devUsersPanel);

  const vmsCountPanel = new StatBuilder()
    .title("Total VMs")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`vms_total{stage="production"}`)
        .legendFormat("VMs")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(vmsCountPanel);

  // Users and VMs over time
  const usersOverTimePanel = new TimeseriesBuilder()
    .title("Users Over Time")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`users_total{type="login",stage="production"}`)
        .legendFormat("{{stage}} login")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`users_total{type="dev",stage="production"}`)
        .legendFormat("{{stage}} dev")
    );
  dash.withPanel(usersOverTimePanel);

  const vmsOverTimePanel = new TimeseriesBuilder()
    .title("VMs Over Time")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`vms_total{stage="production"}`)
        .legendFormat("{{stage}}")
    );
  dash.withPanel(vmsOverTimePanel);

  // Proxy Bytes
  dash.withRow(
    new RowBuilder("Proxy Bytes").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Proxy Bytes Rate",
    `sum(rate(proxy_bytes_total{${STAGE_FILTER}}[$__rate_interval])) by (direction, stage)`,
    {
      panelCustomization: (x) => x.unit("Bps").min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}} {{direction}}"),
    }
  );

  addTimeseriesChart(
    "Proxy Bytes Total",
    `sum(increase(proxy_bytes_total{${STAGE_FILTER}}[1h])) by (direction, stage)`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}} {{direction}}"),
    }
  );

  // ========== EXELETS (Container Hosts) ==========
  dash.withRow(
    new RowBuilder("Exelets (Container Hosts)").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Exelet CPU Usage",
    `100 - (avg by (instance) (irate(node_cpu_seconds_total{role="exelet",${STAGE_FILTER},mode="idle"}[5m])) * 100)`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  addTimeseriesChart(
    "Exelet Memory Usage",
    `(1 - (node_memory_MemAvailable_bytes{role="exelet",${STAGE_FILTER}} / node_memory_MemTotal_bytes{role="exelet",${STAGE_FILTER}})) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  addTimeseriesChart(
    "Exelet Memory Pressure",
    `rate(node_pressure_memory_waiting_seconds_total{role="exelet",${STAGE_FILTER}}[5m]) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").min(0),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  addTimeseriesChart(
    "Exelet Error Logs",
    `sum(increase(logs_total{job="exelet",level="ERROR",${STAGE_FILTER}}[$__rate_interval])) by (instance)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  // ========== EXED (Web/SSH Frontend) ==========
  dash.withRow(
    new RowBuilder("Exed (Web/SSH Frontend)").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Exed CPU Usage",
    `100 - (avg by (instance) (irate(node_cpu_seconds_total{role="exed",${STAGE_FILTER},mode="idle"}[5m])) * 100)`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  addTimeseriesChart(
    "Exed Memory Usage",
    `(1 - (node_memory_MemAvailable_bytes{role="exed",${STAGE_FILTER}} / node_memory_MemTotal_bytes{role="exed",${STAGE_FILTER}})) * 100`,
    {
      panelCustomization: (x) => x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  const exedNetworkPanel = new TimeseriesBuilder()
    .title("Exed Network Traffic")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 6, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`irate(node_network_receive_bytes_total{role="exed",${STAGE_FILTER},device!="lo"}[5m])`)
        .legendFormat("{{instance}} rx")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`irate(node_network_transmit_bytes_total{role="exed",${STAGE_FILTER},device!="lo"}[5m])`)
        .legendFormat("{{instance}} tx")
    );
  dash.withPanel(exedNetworkPanel);

  addTimeseriesChart(
    "Exed Error Logs",
    `sum(increase(logs_total{job="exed",level="ERROR",${STAGE_FILTER}}[$__rate_interval])) by (instance)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 6, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}}"),
    }
  );

  // ========== LOGGING ==========
  dash.withRow(
    new RowBuilder("Logging").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Warning Logs",
    `sum(increase(logs_total{level="WARN",${STAGE_FILTER}}[$__rate_interval])) by (job, stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}} {{job}}"),
    }
  );

  addTimeseriesChart(
    "Error Logs",
    `sum(increase(logs_total{level="ERROR",${STAGE_FILTER}}[$__rate_interval])) by (job, stage)`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{stage}} {{job}}"),
    }
  );

  const logsOverTimePanel = new TimeseriesBuilder()
    .title("Logs by Level")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .stacking(
      new StackingConfigBuilder()
        .mode(StackingMode.Normal)
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(logs_total{${STAGE_FILTER}}[$__rate_interval])) by (level)`)
        .legendFormat("{{level}}")
    );
  dash.withPanel(logsOverTimePanel);

  return dash;
}

function makeGrafanaDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("Grafana Self-Monitoring Dashboard");
  dash
    .uid("grafana-monitoring-dashboard")
    .tags(["generated", "grafana", "monitoring"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  const addTimeseriesChart = makeAddTimeseriesChart(
    dash,
    "grafana-monitoring-dashboard"
  );

  // README panel for auto-generated dashboard
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Row 1: Request Metrics
  dash.withRow(
    new RowBuilder("Request Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "HTTP Request Rate",
    `rate(grafana_http_request_duration_seconds_count[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "HTTP Request Latency (p95)",
    `histogram_quantile(0.95, sum(rate(grafana_http_request_duration_seconds_bucket[5m])) by (le))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "HTTP Request Latency (p99)",
    `histogram_quantile(0.99, sum(rate(grafana_http_request_duration_seconds_bucket[5m])) by (le))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 2: Resource Usage
  dash.withRow(
    new RowBuilder("Resource Usage").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Process CPU Usage",
    `rate(process_cpu_seconds_total{job="grafana"}[5m]) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Process Memory Usage",
    `process_resident_memory_bytes{job="grafana"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Go Heap Allocated",
    `go_memstats_heap_alloc_bytes{job="grafana"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes"),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 3: Goroutines and GC
  dash.withRow(
    new RowBuilder("Go Runtime Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Number of Goroutines",
    `go_goroutines{job="grafana"}`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "GC Rate",
    `rate(go_gc_duration_seconds_count{job="grafana"}[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "GC Duration (p95)",
    `histogram_quantile(0.95, rate(go_gc_duration_seconds_bucket{job="grafana"}[5m]))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 4: Database Performance
  dash.withRow(
    new RowBuilder("Database Performance").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Database Query Rate",
    `rate(grafana_database_queries_total[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Database Query Duration (p95)",
    `histogram_quantile(0.95, sum(rate(grafana_database_query_duration_seconds_bucket[5m])) by (le))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Database Connection Pool",
    `grafana_database_connections_open{job="grafana"}`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 5: Alert Manager
  dash.withRow(
    new RowBuilder("Alert Manager").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Active Alerts",
    `grafana_alerting_active_configurations{job="grafana"}`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Alert Rule Evaluations Rate",
    `rate(grafana_alerting_rule_evaluations_total[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Alert Notification Rate",
    `rate(grafana_alerting_notifications_sent_total[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 6: API and Dashboard Metrics
  dash.withRow(
    new RowBuilder("Dashboard Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Dashboard API Requests",
    `rate(grafana_api_dashboard_get_duration_seconds_count[5m])`,
    {
      gridPos: { w: 12, h: 6 },
    }
  );

  addTimeseriesChart(
    "Dashboard Load Latency (p95)",
    `histogram_quantile(0.95, rate(grafana_api_dashboard_get_duration_seconds_bucket[5m]))`,
    {
      panelCustomization: (x) =>
        x.unit("s"),
      gridPos: { w: 12, h: 6 },
    }
  );

  return dash;
}

function makeMonMonDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("Mon Mon - Monitoring Infrastructure");
  dash
    .uid("mon-mon-dashboard")
    .tags(["generated", "monitoring", "infrastructure"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  const addTimeseriesChart = makeAddTimeseriesChart(
    dash,
    "mon-mon-dashboard"
  );

  // README panel for auto-generated dashboard
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Row 1: Overview Stats
  dash.withRow(
    new RowBuilder("Monitoring Server Overview").gridPos(gp({ w: 24, h: 1 }))
  );

  // Mon host memory stat
  const monMemoryPanel = new StatBuilder()
    .title("Mon Host Memory Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`(1 - (node_memory_MemAvailable_bytes{instance="mon:9100"} / node_memory_MemTotal_bytes{instance="mon:9100"})) * 100`)
        .legendFormat("Memory %")
    )
    .unit("percent")
    .colorMode(BigValueColorMode.None)
    .graphMode(BigValueGraphMode.None)
    .textMode(BigValueTextMode.Value)
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 80, color: "yellow" } as Threshold,
          { value: 90, color: "red" } as Threshold,
        ])
    )
    .min(0)
    .max(100);
  dash.withPanel(monMemoryPanel);

  // Grafana memory stat
  const grafanaMemoryPanel = new StatBuilder()
    .title("Grafana Memory Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`process_resident_memory_bytes{job="grafana"}`)
        .legendFormat("Memory")
    )
    .unit("bytes")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.None)
    .textMode(BigValueTextMode.Value)
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1.5 * 1024 * 1024 * 1024, color: "yellow" } as Threshold,
          { value: 2.0 * 1024 * 1024 * 1024, color: "red" } as Threshold,
        ])
    )
    .min(0);
  dash.withPanel(grafanaMemoryPanel);

  // Prometheus memory stat
  const prometheusMemoryPanel = new StatBuilder()
    .title("Prometheus Memory Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`process_resident_memory_bytes{job="prometheus"}`)
        .legendFormat("Memory")
    )
    .unit("bytes")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.None)
    .textMode(BigValueTextMode.Value)
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1.5 * 1024 * 1024 * 1024, color: "yellow" } as Threshold,
          { value: 2.0 * 1024 * 1024 * 1024, color: "red" } as Threshold,
        ])
    )
    .min(0);
  dash.withPanel(prometheusMemoryPanel);

  // Mon host CPU stat
  const monCpuPanel = new StatBuilder()
    .title("Mon Host CPU Usage")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`100 - (avg(rate(node_cpu_seconds_total{instance="mon:9100",mode="idle"}[5m])) * 100)`)
        .legendFormat("CPU %")
    )
    .unit("percent")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.None)
    .textMode(BigValueTextMode.Value)
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 70, color: "yellow" } as Threshold,
          { value: 85, color: "red" } as Threshold,
        ])
    )
    .min(0)
    .max(100);
  dash.withPanel(monCpuPanel);

  // Row 2: Host Memory Details
  dash.withRow(
    new RowBuilder("Mon Host Memory Details").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Mon Host Memory Usage %",
    `(1 - (node_memory_MemAvailable_bytes{instance="mon:9100"} / node_memory_MemTotal_bytes{instance="mon:9100"})) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 12, h: 6 },
    }
  );

  addTimeseriesChart(
    "Mon Host Memory (Bytes)",
    `node_memory_MemTotal_bytes{instance="mon:9100"} - node_memory_MemAvailable_bytes{instance="mon:9100"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 12, h: 6 },
    }
  );

  // Row 3: Process Memory
  dash.withRow(
    new RowBuilder("Process Memory Usage").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Grafana Memory Usage",
    `process_resident_memory_bytes{job="grafana"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Prometheus Memory Usage",
    `process_resident_memory_bytes{job="prometheus"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Combined Monitoring Memory",
    `sum(process_resident_memory_bytes{job=~"grafana|prometheus"})`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 4: CPU Usage
  dash.withRow(
    new RowBuilder("CPU Usage").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Mon Host CPU Usage",
    `100 - (avg(rate(node_cpu_seconds_total{instance="mon:9100",mode="idle"}[5m])) * 100)`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Grafana CPU Usage",
    `rate(process_cpu_seconds_total{job="grafana"}[5m]) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Prometheus CPU Usage",
    `rate(process_cpu_seconds_total{job="prometheus"}[5m]) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 5: Grafana Key Metrics
  dash.withRow(
    new RowBuilder("Grafana Performance").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Grafana HTTP Request Rate",
    `rate(grafana_http_request_duration_seconds_count{job="grafana"}[5m])`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Grafana Request Latency p95",
    `histogram_quantile(0.95, sum(rate(grafana_http_request_duration_seconds_bucket{job="grafana"}[5m])) by (le))`,
    {
      panelCustomization: (x) =>
        x.unit("s").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Grafana Goroutines",
    `go_goroutines{job="grafana"}`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 6: Prometheus Key Metrics
  dash.withRow(
    new RowBuilder("Prometheus Performance").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Prometheus Samples Ingested Rate",
    `rate(prometheus_tsdb_head_samples_appended_total{job="prometheus"}[5m])`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Prometheus Active Series",
    `prometheus_tsdb_head_series{job="prometheus"}`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Prometheus Query Duration p95",
    `histogram_quantile(0.95, rate(prometheus_engine_query_duration_seconds_bucket{job="prometheus"}[5m]))`,
    {
      panelCustomization: (x) =>
        x.unit("s").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Row 7: Storage and Resources
  dash.withRow(
    new RowBuilder("Storage & Resources").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Mon Host Disk Usage %",
    `(1 - (node_filesystem_avail_bytes{instance="mon:9100",fstype!="tmpfs",fstype!="devtmpfs"} / node_filesystem_size_bytes{instance="mon:9100",fstype!="tmpfs",fstype!="devtmpfs"})) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{mountpoint}}"),
    }
  );

  addTimeseriesChart(
    "Prometheus TSDB Size",
    `prometheus_tsdb_storage_blocks_bytes{job="prometheus"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Mon Host Network I/O",
    `rate(node_network_receive_bytes_total{instance="mon:9100",device!="lo"}[5m])`,
    {
      panelCustomization: (x) =>
        x.unit("Bps").min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("RX {{device}}"),
    }
  );

  // Row 8: OpenTelemetry Collector
  dash.withRow(
    new RowBuilder("OpenTelemetry Collector").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "OTel Collector Memory Usage",
    `otelcol_process_memory_rss_bytes{job="otel-collector"}`,
    {
      panelCustomization: (x) =>
        x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "OTel Collector CPU Usage",
    `rate(otelcol_process_cpu_seconds_total{job="otel-collector"}[5m]) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "OTel Collector Logs Received Rate",
    `rate(otelcol_receiver_accepted_log_records_total{job="otel-collector"}[5m])`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "OTel Collector Logs Exported Rate",
    `rate(otelcol_exporter_sent_log_records_total{job="otel-collector"}[5m])`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{exporter}}"),
    }
  );

  addTimeseriesChart(
    "OTel Collector Export Failures",
    `rate(otelcol_exporter_send_failed_log_records_total{job="otel-collector"}[5m])`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{exporter}}"),
    }
  );

  return dash;
}

// Helper method to create "addTimeseriesChart" methods for your dashboard.
function makeAddTimeseriesChart(dash: DashboardBuilder, dashboardUID: string) {
  const builders = {
    buildPanel: () => new TimeseriesBuilder(),
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
        }
      );

      if (existingAlertResponse.ok) {
        console.log(
          `🔄 Updating existing alert for ${alertSpec.panelTitle}...`
        );
        await fetch(
          `${GRAFANA_URL}api/v1/provisioning/alert-rules/${alertUID}`,
          {
            method: "DELETE",
            headers: {
              Authorization: `Bearer ${TOKEN}`,
            },
          }
        );
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
        noDataState: alertSpec.alertConfig.noDataState || "NoData",
        execErrState: "Alerting",
        for: alertSpec.alertConfig.forDuration || "1m",
        ruleGroup: "dashboard-alerts",
        annotations: {
          summary:
            alertSpec.alertConfig.summary ||
            `${alertSpec.panelTitle} has exceeded threshold`,
          description:
            alertSpec.alertConfig.description ||
            `${alertSpec.panelTitle} is above ${alertSpec.alertConfig.threshold}`,
        },
        labels: {
          panel: alertSpec.panelTitle,
          dashboard: alertSpec.dashboardUID,
          ...alertSpec.alertConfig.labels,
        },
        folderUID: "auto-alerts",
      };

      const response = await fetch(
        `${GRAFANA_URL}api/v1/provisioning/alert-rules`,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${TOKEN}`,
          },
          body: JSON.stringify(alertRule),
        }
      );

      if (response.ok) {
        console.log(`✓ Created alert for ${alertSpec.panelTitle}`);
      } else {
        const errorText = await response.text();
        console.error(
          `✗ Failed to create alert for ${alertSpec.panelTitle}: ${response.status} - ${errorText}`
        );
      }
    } catch (error) {
      console.error(
        `✗ Error creating alert for ${alertSpec.panelTitle}:`,
        error
      );
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
  // Only alert on production stage
  return query
    .replace(/instance=~"\$instance"/g, 'instance=~".+"')
    .replace(/tag_Name=~"\$name"/g, 'tag_Name=~".+"')
    .replace(/tag_role=~"\$role"/g, 'tag_role=~".+"')
    .replace(/tag_stage=~"\$stage"/g, 'tag_stage="production"');
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
        (ds: any) => ds.type === "prometheus" && ds.isDefault
      );

      if (defaultPrometheus) {
        defaultPrometheusDatasourceUID = defaultPrometheus.uid;
        return defaultPrometheus.uid;
      } else {
        // Fallback to first Prometheus datasource if no default
        const firstPrometheus = datasources.find(
          (ds: any) => ds.type === "prometheus"
        );
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
        console.error(
          "✗ Failed to create alerts folder:",
          await createResponse.text()
        );
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
    const getResponse = await fetch(
      `${GRAFANA_URL}api/dashboards/uid/${built.uid}`,
      {
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${TOKEN}`,
        },
      }
    );
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
      const baseUrl = GRAFANA_URL?.endsWith("/")
        ? GRAFANA_URL.slice(0, -1)
        : GRAFANA_URL;
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
  dashboardUID: string
) {
  return function addChart(
    title: string,
    query: string,
    {
      panelCustomization,
      queryCustomization,
      alert,
      alertQueryOverride,
      gridPos: gridPosSize,
    }: {
      panelCustomization?: (panel: T) => T;
      queryCustomization?: (dataQuery: DataqueryBuilder) => DataqueryBuilder;
      alert?: AlertConfig;
      alertQueryOverride?: string; // Use a different query for alerts (e.g., to exclude certain hosts)
      gridPos?: { w: number; h: number };
    } = {}
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
        GraphThresholdsStyleMode.Dashed
      );

      panel.thresholds(thresholds).thresholdsStyle(thresholdStyle);

      // Store alert configuration for later creation
      // Use alertQueryOverride if provided (e.g., to exclude certain hosts from alerting)
      alertsToCreate.push({
        panelTitle: title,
        query: alertQueryOverride || query,
        alertConfig: alert,
        dashboardUID: dashboardUID,
      });
    }

    // Apply panel customization if provided (after threshold configuration)
    if (panelCustomization) {
      panelCustomization(panel);
    }

    // Apply gridPos (default 8x6 if not specified)
    panel.gridPos(gp(gridPosSize ?? { w: 8, h: 6 }));

    // Attach the query target to the panel
    panel.withTarget(queryTarget);
    dash.withPanel(panel);
  };
}

// Hosts Dashboard - node exporter metrics across all hosts
function makeHostsDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("Hosts Dashboard");
  dash
    .uid("hosts-dashboard")
    .tags(["generated", "hosts", "node-exporter"])
    .refresh("1m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  const addTimeseriesChart = makeAddTimeseriesChart(dash, "hosts-dashboard");

  // Host metrics use this filter
  const HOST_FILTER = 'instance=~"$instance",role=~"$role",stage=~"$stage"';

  // Variable definition for instance selection from node exporter metrics
  dash.withVariable(
    new QueryVariableBuilder("instance")
      .includeAll(true)
      .query("label_values(node_uname_info,instance)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  // Variable definition for role selection
  dash.withVariable(
    new QueryVariableBuilder("role")
      .includeAll(true)
      .query("label_values(node_uname_info,role)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  // Variable definition for stage selection
  dash.withVariable(
    new QueryVariableBuilder("stage")
      .includeAll(true)
      .query("label_values(node_uname_info,stage)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  // README panel
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // CPU Metrics Row
  dash.withRow(
    new RowBuilder("CPU Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "CPU Usage %",
    `100 - (avg by (instance) (irate(node_cpu_seconds_total{${HOST_FILTER},mode="idle"}[5m])) * 100)`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart("Load Average", `node_load1{${HOST_FILTER}}`, {
    gridPos: { w: 8, h: 6 },
  });

  addTimeseriesChart(
    "CPU Count",
    `count by (instance) (node_cpu_seconds_total{${HOST_FILTER},mode="idle"})`,
    {
      panelCustomization: (x) => x.min(0),
      gridPos: { w: 8, h: 6 },
    }
  );

  // Memory Metrics Row
  dash.withRow(
    new RowBuilder("Memory Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  // Memory Available % - alert only for non-exelet hosts
  addTimeseriesChart(
    "Memory Available %",
    `(node_memory_MemAvailable_bytes{${HOST_FILTER}} / node_memory_MemTotal_bytes{${HOST_FILTER}}) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      alert: {
        threshold: 20,
        condition: "lt",
        forDuration: "3m",
        summary: "Memory available is critically low",
        description: "Memory available has dropped below 20% for more than 3 minutes",
      },
      // Alert query excludes exelet hosts - they are allowed to run out of memory
      alertQueryOverride: `(node_memory_MemAvailable_bytes{role!="exelet"} / node_memory_MemTotal_bytes{role!="exelet"}) * 100`,
    }
  );

  // Memory Usage % - alert only for non-exelet hosts
  addTimeseriesChart(
    "Memory Usage %",
    `(1 - (node_memory_MemAvailable_bytes{${HOST_FILTER}} / node_memory_MemTotal_bytes{${HOST_FILTER}})) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 6, h: 6 },
      alert: {
        threshold: 90,
        condition: "gt",
        forDuration: "3m",
        summary: "Memory usage is critically high",
        description: "Memory usage has exceeded 90% for more than 3 minutes",
      },
      // Alert query excludes exelet hosts - they are allowed to run out of memory
      alertQueryOverride: `(1 - (node_memory_MemAvailable_bytes{role!="exelet"} / node_memory_MemTotal_bytes{role!="exelet"})) * 100`,
    }
  );

  addTimeseriesChart("Memory Total", `node_memory_MemTotal_bytes{${HOST_FILTER}}`, {
    panelCustomization: (x) => x.unit("bytes"),
      gridPos: { w: 6, h: 6 },
  });

  addTimeseriesChart("Memory Available", `node_memory_MemAvailable_bytes{${HOST_FILTER}}`, {
    panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 6, h: 6 },
  });

  // Memory Usage by Type (stacked area chart)
  // Shows all major memory categories from /proc/meminfo, summed across hosts
  const memoryByTypePanel = new TimeseriesBuilder()
    .title("Memory Usage by Type")
    .unit("bytes")
    .min(0)
    .gridPos(gp({ w: 24, h: 8 }))
    .stacking(new StackingConfigBuilder().mode(StackingMode.Normal))
    .fillOpacity(80)
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_AnonPages_bytes{${HOST_FILTER}})`)
        .legendFormat("AnonPages")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Cached_bytes{${HOST_FILTER}} - node_memory_Shmem_bytes{${HOST_FILTER}})`)
        .legendFormat("Cached")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Shmem_bytes{${HOST_FILTER}})`)
        .legendFormat("Shmem")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Buffers_bytes{${HOST_FILTER}})`)
        .legendFormat("Buffers")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_SReclaimable_bytes{${HOST_FILTER}})`)
        .legendFormat("SReclaimable")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_SUnreclaim_bytes{${HOST_FILTER}})`)
        .legendFormat("SUnreclaim")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_KernelStack_bytes{${HOST_FILTER}})`)
        .legendFormat("KernelStack")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_PageTables_bytes{${HOST_FILTER}})`)
        .legendFormat("PageTables")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Hugetlb_bytes{${HOST_FILTER}})`)
        .legendFormat("Hugetlb")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Percpu_bytes{${HOST_FILTER}})`)
        .legendFormat("Percpu")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_Bounce_bytes{${HOST_FILTER}})`)
        .legendFormat("Bounce")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_WritebackTmp_bytes{${HOST_FILTER}})`)
        .legendFormat("WritebackTmp")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(node_memory_MemFree_bytes{${HOST_FILTER}})`)
        .legendFormat("Free")
    );
  dash.withPanel(memoryByTypePanel);

  // Resource Pressure (PSI) Row - moved here to be right after memory metrics
  dash.withRow(
    new RowBuilder("Resource Pressure (PSI)").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "CPU Pressure",
    `rate(node_pressure_cpu_waiting_seconds_total{${HOST_FILTER}}[5m]) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0),
      gridPos: { w: 8, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}} waiting"),
    }
  );

  // IO Pressure - both waiting and stalled
  const ioPressurePanel = new TimeseriesBuilder()
    .title("IO Pressure")
    .unit("percent")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`rate(node_pressure_io_waiting_seconds_total{${HOST_FILTER}}[5m]) * 100`)
        .legendFormat("{{instance}} waiting")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`rate(node_pressure_io_stalled_seconds_total{${HOST_FILTER}}[5m]) * 100`)
        .legendFormat("{{instance}} stalled")
    );
  dash.withPanel(ioPressurePanel);

  // Memory Pressure - both waiting and stalled
  const memoryPressurePanel = new TimeseriesBuilder()
    .title("Memory Pressure")
    .unit("percent")
    .min(0)
    .gridPos(gp({ w: 8, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`rate(node_pressure_memory_waiting_seconds_total{${HOST_FILTER}}[5m]) * 100`)
        .legendFormat("{{instance}} waiting")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`rate(node_pressure_memory_stalled_seconds_total{${HOST_FILTER}}[5m]) * 100`)
        .legendFormat("{{instance}} stalled")
    );
  dash.withPanel(memoryPressurePanel);

  // Swap Memory Row
  dash.withRow(
    new RowBuilder("Swap Memory").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Swap Usage %",
    `(1 - (node_memory_SwapFree_bytes{${HOST_FILTER}} / node_memory_SwapTotal_bytes{${HOST_FILTER}})) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
      alert: {
        threshold: 75,
        condition: "gt",
        forDuration: "3m",
        noDataState: "OK",
        summary: "Swap usage is high",
        description: "Swap usage has exceeded 75% for more than 3 minutes",
        labels: { signal: "strong" },
      },
      // Alert query filters out hosts with no swap (SwapTotal == 0) and only alerts for exelets
      alertQueryOverride: `(1 - (node_memory_SwapFree_bytes{role="exelet"} / node_memory_SwapTotal_bytes{role="exelet"})) * 100 and node_memory_SwapTotal_bytes{role="exelet"} > 0`,
    }
  );

  addTimeseriesChart("Swap Total", `node_memory_SwapTotal_bytes{${HOST_FILTER}}`, {
    panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
  });

  addTimeseriesChart("Swap Used", `node_memory_SwapTotal_bytes{${HOST_FILTER}} - node_memory_SwapFree_bytes{${HOST_FILTER}}`, {
    panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
  });

  // Disk Metrics Row
  dash.withRow(
    new RowBuilder("Disk Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Disk Usage %",
    `(1 - (node_filesystem_avail_bytes{${HOST_FILTER},fstype!="tmpfs",fstype!="devtmpfs"} / node_filesystem_size_bytes{${HOST_FILTER},fstype!="tmpfs",fstype!="devtmpfs"})) * 100`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 8, h: 6 },
      alert: {
        threshold: 80,
        condition: "gt",
        forDuration: "1m",
        summary: "Disk usage is critically high",
        description: "Disk usage has exceeded 80% for more than 1 minute",
      },
    }
  );

  // Exelet /data disk space alert - alert when less than 50GB available
  addTimeseriesChart(
    "Exelet /data Available",
    `node_filesystem_avail_bytes{${HOST_FILTER},mountpoint="/data"}`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 8, h: 6 },
      alert: {
        threshold: 50 * 1024 * 1024 * 1024, // 50GB in bytes
        condition: "lt",
        forDuration: "1m",
        summary: "Exelet /data disk space is critically low",
        description: "Exelet /data filesystem has less than 50GB available",
        labels: { signal: "strong" },
      },
      alertQueryOverride: `node_filesystem_avail_bytes{role="exelet",mountpoint="/data"}`,
    }
  );

  addTimeseriesChart("Disk I/O Read", `irate(node_disk_read_bytes_total{${HOST_FILTER}}[5m])`, {
    panelCustomization: (x) => x.unit("Bps"),
      gridPos: { w: 8, h: 6 },
  });

  addTimeseriesChart("Disk I/O Write", `irate(node_disk_written_bytes_total{${HOST_FILTER}}[5m])`, {
    panelCustomization: (x) => x.unit("Bps"),
      gridPos: { w: 8, h: 6 },
  });

  // Network Metrics Row
  dash.withRow(
    new RowBuilder("Network Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Network Receive",
    `irate(node_network_receive_bytes_total{${HOST_FILTER},device!="lo"}[5m])`,
    {
      panelCustomization: (x) => x.unit("Bps"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Network Transmit",
    `irate(node_network_transmit_bytes_total{${HOST_FILTER},device!="lo"}[5m])`,
    {
      panelCustomization: (x) => x.unit("Bps"),
      gridPos: { w: 8, h: 6 },
    }
  );

  addTimeseriesChart(
    "Network Errors",
    `irate(node_network_receive_errs_total{${HOST_FILTER},device!="lo"}[5m]) + irate(node_network_transmit_errs_total{${HOST_FILTER},device!="lo"}[5m])`,
    {
      gridPos: { w: 8, h: 6 },
    }
  );

  // Huge Pages Row - at the bottom
  dash.withRow(
    new RowBuilder("Huge Pages").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Huge Pages Used vs Total",
    `(node_memory_HugePages_Total{${HOST_FILTER}} - node_memory_HugePages_Free{${HOST_FILTER}}) * node_memory_Hugepagesize_bytes{${HOST_FILTER}}`,
    {
      panelCustomization: (x) => x.unit("bytes").min(0),
      gridPos: { w: 12, h: 6 },
      queryCustomization: (q) => q.legendFormat("{{instance}} Used"),
    }
  );

  // Second target for total huge pages capacity as separate chart
  const hugePagesPanel = new TimeseriesBuilder()
    .title("Huge Pages Capacity")
    .unit("bytes")
    .min(0)
    .gridPos(gp({ w: 12, h: 6 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`node_memory_HugePages_Total{${HOST_FILTER}} * node_memory_Hugepagesize_bytes{${HOST_FILTER}}`)
        .legendFormat("{{instance}} Total")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`node_memory_HugePages_Free{${HOST_FILTER}} * node_memory_Hugepagesize_bytes{${HOST_FILTER}}`)
        .legendFormat("{{instance}} Free")
    );
  dash.withPanel(hugePagesPanel);

  // Alert: Huge pages > 0 AND at 80% capacity
  // This query returns a value only when huge pages are in use AND usage is >= 80%
  addTimeseriesChart(
    "Huge Pages Usage %",
    `((node_memory_HugePages_Total{${HOST_FILTER}} - node_memory_HugePages_Free{${HOST_FILTER}}) / node_memory_HugePages_Total{${HOST_FILTER}}) * 100 and on(instance) (node_memory_HugePages_Total{${HOST_FILTER}} - node_memory_HugePages_Free{${HOST_FILTER}}) > 0`,
    {
      panelCustomization: (x) =>
        x.unit("percent").min(0).max(100),
      gridPos: { w: 12, h: 6 },
      alert: {
        threshold: 80,
        condition: "gt",
        forDuration: "3m",
        summary: "Huge pages usage is critically high",
        description: "Huge pages are in use and usage has exceeded 80% for more than 3 minutes",
      },
    }
  );

  return dash;
}

// AWS CloudWatch Dashboard - metrics from YACE (Yet Another CloudWatch Exporter)
function makeAwsCloudWatchDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("AWS CloudWatch");
  dash
    .uid("aws-cloudwatch-dashboard")
    .tags(["generated", "aws", "cloudwatch"])
    .refresh("5m")
    .time({ from: "now-6h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  const addTimeseriesChart = makeAddTimeseriesChart(dash, "aws-cloudwatch-dashboard");

  // Filter for AWS metrics (name, role, stage)
  const AWS_FILTER = 'tag_Name=~"$name",tag_role=~"$role",tag_stage=~"$stage"';

  // Variable definitions for filtering
  dash.withVariable(
    new QueryVariableBuilder("name")
      .includeAll(true)
      .query("label_values(aws_ec2_cpuutilization_average,tag_Name)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  dash.withVariable(
    new QueryVariableBuilder("role")
      .includeAll(true)
      .query("label_values(aws_ec2_cpuutilization_average,tag_role)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  dash.withVariable(
    new QueryVariableBuilder("stage")
      .includeAll(true)
      .query("label_values(aws_ec2_cpuutilization_average,tag_stage)")
      .current({ text: "All", value: "$__all" })
      .multi(true)
  );

  // README panel
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // ========== EC2 SECTION ==========
  dash.withRow(
    new RowBuilder("EC2 Instances").gridPos(gp({ w: 24, h: 1 }))
  );

  // Row 1: CPU metrics
  // CPU Utilization (Average)
  const ec2CpuPanel = new TimeseriesBuilder()
    .title("CPU Utilization (Average)")
    .unit("percent")
    .min(0)
    .max(100)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_cpuutilization_average{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2CpuPanel);

  // CPU Utilization (Maximum)
  const ec2CpuMaxPanel = new TimeseriesBuilder()
    .title("CPU Utilization (Maximum)")
    .unit("percent")
    .min(0)
    .max(100)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_cpuutilization_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2CpuMaxPanel);

  // Status Checks (combined panel)
  const ec2StatusPanel = new TimeseriesBuilder()
    .title("Status Checks (should be 0)")
    .unit("short")
    .min(0)
    .max(1)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_status_check_failed_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} any")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_status_check_failed_instance_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} instance")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_status_check_failed_system_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} system")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_status_check_failed_attached_ebs_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} EBS")
    )
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1, color: "red" } as Threshold,
        ])
    );
  dash.withPanel(ec2StatusPanel);

  // Row 2: Network throughput
  // Network In (bytes/sec)
  const ec2NetInPanel = new TimeseriesBuilder()
    .title("Network In")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_network_in_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2NetInPanel);

  // Network Out (bytes/sec)
  const ec2NetOutPanel = new TimeseriesBuilder()
    .title("Network Out")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_network_out_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2NetOutPanel);

  // Network Packets (combined in/out)
  const ec2NetPacketsPanel = new TimeseriesBuilder()
    .title("Network Packets")
    .unit("pps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_network_packets_in_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} in")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_network_packets_out_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} out")
    );
  dash.withPanel(ec2NetPacketsPanel);

  // Row 3: Instance store disk I/O (for instances with local disks)
  // Disk Read Throughput
  const ec2DiskReadPanel = new TimeseriesBuilder()
    .title("Disk Read Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_disk_read_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2DiskReadPanel);

  // Disk Write Throughput
  const ec2DiskWritePanel = new TimeseriesBuilder()
    .title("Disk Write Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_disk_write_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2DiskWritePanel);

  // Disk IOPS
  const ec2DiskOpsPanel = new TimeseriesBuilder()
    .title("Disk IOPS")
    .unit("iops")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_disk_read_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} read")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_disk_write_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} write")
    );
  dash.withPanel(ec2DiskOpsPanel);

  // Row 4: EBS metrics (aggregated across all volumes per instance)
  // EBS Read Throughput
  const ec2EbsReadPanel = new TimeseriesBuilder()
    .title("EBS Read Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_ebsread_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2EbsReadPanel);

  // EBS Write Throughput
  const ec2EbsWritePanel = new TimeseriesBuilder()
    .title("EBS Write Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_ebswrite_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ec2EbsWritePanel);

  // EBS IOPS
  const ec2EbsOpsPanel = new TimeseriesBuilder()
    .title("EBS IOPS")
    .unit("iops")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_ebsread_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} read")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_ebswrite_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}} write")
    );
  dash.withPanel(ec2EbsOpsPanel);

  // Row 5: EBS health checks
  // EBS IOPS Exceeded Check
  const ec2EbsIopsExceededPanel = new TimeseriesBuilder()
    .title("EBS IOPS Exceeded")
    .unit("short")
    .min(0)
    .max(1)
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_instance_ebsiopsexceeded_check_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    )
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1, color: "red" } as Threshold,
        ])
    );
  dash.withPanel(ec2EbsIopsExceededPanel);

  // EBS Throughput Exceeded Check
  const ec2EbsThroughputExceededPanel = new TimeseriesBuilder()
    .title("EBS Throughput Exceeded")
    .unit("short")
    .min(0)
    .max(1)
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ec2_instance_ebsthroughput_exceeded_check_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    )
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1, color: "red" } as Threshold,
        ])
    );
  dash.withPanel(ec2EbsThroughputExceededPanel);

  // ========== EBS SECTION ==========
  dash.withRow(
    new RowBuilder("EBS Volumes").gridPos(gp({ w: 24, h: 1 }))
  );

  // Row 1: Latency metrics (most important for performance)
  // Average Read Latency (ms) = TotalReadTime / ReadOps * 1000
  const ebsReadLatencyPanel = new TimeseriesBuilder()
    .title("Average Read Latency")
    .unit("ms")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`(aws_ebs_volume_total_read_time_sum{${AWS_FILTER}} / aws_ebs_volume_read_ops_sum{${AWS_FILTER}}) * 1000`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsReadLatencyPanel);

  // Average Write Latency (ms) = TotalWriteTime / WriteOps * 1000
  const ebsWriteLatencyPanel = new TimeseriesBuilder()
    .title("Average Write Latency")
    .unit("ms")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`(aws_ebs_volume_total_write_time_sum{${AWS_FILTER}} / aws_ebs_volume_write_ops_sum{${AWS_FILTER}}) * 1000`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsWriteLatencyPanel);

  // Average Queue Length
  const ebsQueueLengthPanel = new TimeseriesBuilder()
    .title("Average Queue Length")
    .unit("short")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_queue_length_average{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsQueueLengthPanel);

  // Row 2: Throughput metrics
  // Read Throughput (bytes/sec over 5 min period)
  const ebsReadThroughputPanel = new TimeseriesBuilder()
    .title("Read Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_read_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsReadThroughputPanel);

  // Write Throughput (bytes/sec over 5 min period)
  const ebsWriteThroughputPanel = new TimeseriesBuilder()
    .title("Write Throughput")
    .unit("Bps")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_write_bytes_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsWriteThroughputPanel);

  // Time Spent Idle (%) = IdleTime / Period * 100
  const ebsIdleTimePanel = new TimeseriesBuilder()
    .title("Time Spent Idle")
    .unit("percent")
    .min(0)
    .max(100)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`(aws_ebs_volume_idle_time_sum{${AWS_FILTER}} / 300) * 100`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsIdleTimePanel);

  // Row 3: IOPS metrics
  // Read Operations (ops/sec over 5 min period)
  const ebsReadOpsPanel = new TimeseriesBuilder()
    .title("Read Operations")
    .unit("iops")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_read_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsReadOpsPanel);

  // Write Operations (ops/sec over 5 min period)
  const ebsWriteOpsPanel = new TimeseriesBuilder()
    .title("Write Operations")
    .unit("iops")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_write_ops_sum{${AWS_FILTER}} / 300`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsWriteOpsPanel);

  // EBS Burst Balance
  const ebsBurstPanel = new TimeseriesBuilder()
    .title("Burst Balance")
    .unit("percent")
    .min(0)
    .max(100)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_burst_balance_minimum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    )
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "red" } as Threshold,
          { value: 20, color: "yellow" } as Threshold,
          { value: 50, color: "green" } as Threshold,
        ])
    );
  dash.withPanel(ebsBurstPanel);

  // Row 4: Average I/O size and health checks
  // Average Read Size (bytes/op)
  const ebsAvgReadSizePanel = new TimeseriesBuilder()
    .title("Average Read Size")
    .unit("decbytes")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_read_bytes_sum{${AWS_FILTER}} / aws_ebs_volume_read_ops_sum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsAvgReadSizePanel);

  // Average Write Size (bytes/op)
  const ebsAvgWriteSizePanel = new TimeseriesBuilder()
    .title("Average Write Size")
    .unit("decbytes")
    .min(0)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_write_bytes_sum{${AWS_FILTER}} / aws_ebs_volume_write_ops_sum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}}")
    );
  dash.withPanel(ebsAvgWriteSizePanel);

  // Health checks panel (IOPS exceeded, throughput exceeded, stalled IO)
  const ebsHealthPanel = new TimeseriesBuilder()
    .title("Health Checks (should be 0)")
    .unit("short")
    .min(0)
    .max(1)
    .gridPos(gp({ w: 8, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_iopsexceeded_check_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} IOPS exceeded")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_throughput_exceeded_check_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} throughput exceeded")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_ebs_volume_stalled_iocheck_maximum{${AWS_FILTER}}`)
        .legendFormat("{{tag_Name}} stalled IO")
    )
    .thresholds(
      new ThresholdsConfigBuilder()
        .mode(ThresholdsMode.Absolute)
        .steps([
          { value: null, color: "green" } as Threshold,
          { value: 1, color: "red" } as Threshold,
        ])
    );
  dash.withPanel(ebsHealthPanel);

  // EBS IOPS Exceeded Check - alerts when volumes exceed provisioned IOPS
  // Note: noDataState is "OK" because CloudWatch may not emit this metric when
  // volumes are healthy, so no data means no problem.
  // The >= 0 filter excludes NaN values (detached volumes) which would otherwise trigger alerts.
  // Note: EBS volume metrics don't have tag_stage, so we exclude staging by name pattern.
  addTimeseriesChart(
    "EBS IOPS Exceeded (Alert)",
    `aws_ebs_volume_iopsexceeded_check_maximum{tag_Name!~".*staging.*"} >= 0`,
    {
      panelCustomization: (x) => x.min(0).max(1),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{tag_Name}}"),
      alert: {
        threshold: 0,
        condition: "gt",
        forDuration: "30m",
        noDataState: "OK",
        summary: "EBS volume is exceeding provisioned IOPS",
        description: "An EBS volume has exceeded its provisioned IOPS limit",
      },
    }
  );

  // ========== ROUTE53 SECTION ==========
  dash.withRow(
    new RowBuilder("Route53 DNS").gridPos(gp({ w: 24, h: 1 }))
  );

  // Route53 DNS Queries
  const route53Panel = new TimeseriesBuilder()
    .title("Route53 DNS Queries (5m)")
    .min(0)
    .gridPos(gp({ w: 24, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`aws_route53_dnsqueries_sum`)
        .legendFormat("{{custom_tag_zone}}")
    );
  dash.withPanel(route53Panel);

  return dash;
}

// exe.dev LLM Gateway Dashboard - token usage, costs, latency, and rate limits
function makeLLMGatewayDashboard() {
  resetLayout();
  const dash = new DashboardBuilder("exe.dev LLM Gateway");
  dash
    .uid("exe-dev-llm-gateway")
    .tags(["generated", "exe.dev", "llm"])
    .refresh("30s")
    .time({ from: "now-1h", to: "now" })
    .tooltip(DashboardCursorSync.Crosshair)
    .timezone("browser");

  addStageVariable(dash);

  // Add model variable
  dash.withVariable(
    new QueryVariableBuilder("model")
      .label("Model")
      .includeAll(true)
      .query('label_values(llm_tokens_total, model)')
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1)
  );

  // Add api_type variable
  dash.withVariable(
    new QueryVariableBuilder("api_type")
      .label("API Type")
      .includeAll(true)
      .query('label_values(llm_tokens_total, api_type)')
      .current({ text: "All", value: "$__all" })
      .multi(true)
      .sort(1)
  );

  const addTimeseriesChart = makeAddTimeseriesChart(dash, "exe-dev-llm-gateway");
  const STAGE_FILTER = 'stage=~"$stage"';
  const MODEL_FILTER = 'model=~"$model"';
  const API_TYPE_FILTER = 'api_type=~"$api_type"';
  const FULL_FILTER = `${STAGE_FILTER},${MODEL_FILTER},${API_TYPE_FILTER}`;

  // README panel
  dash.withPanel(
    new TextPanelBuilder()
      .title("")
      .content(README_CONTENT)
      .mode(TextMode.Markdown)
      .gridPos(gp({ w: 24, h: 2 }))
  );

  // Row 1: Overview Stats
  dash.withRow(
    new RowBuilder("Overview").gridPos(gp({ w: 24, h: 1 }))
  );

  // Total requests (last 24h)
  const totalRequestsPanel = new StatBuilder()
    .title("Requests (24h)")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(increase(llm_requests_total{${STAGE_FILTER}}[24h]))`)
        .legendFormat("Requests")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalRequestsPanel);

  // Total cost (last 24h)
  const totalCostPanel = new StatBuilder()
    .title("Cost (24h)")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(increase(llm_cost_usd_total{${STAGE_FILTER}}[24h]))`)
        .legendFormat("USD")
    )
    .unit("currencyUSD")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalCostPanel);

  // Total tokens (last 24h)
  const totalTokensPanel = new StatBuilder()
    .title("Tokens (24h)")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(increase(llm_tokens_total{${STAGE_FILTER}}[24h]))`)
        .legendFormat("Tokens")
    )
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(totalTokensPanel);

  // Current request rate
  const requestRatePanel = new StatBuilder()
    .title("Request Rate")
    .gridPos(gp({ w: 6, h: 4 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`sum(rate(llm_requests_total{${STAGE_FILTER}}[5m]))`)
        .legendFormat("req/s")
    )
    .unit("reqps")
    .colorMode(BigValueColorMode.Value)
    .graphMode(BigValueGraphMode.Area)
    .textMode(BigValueTextMode.ValueAndName)
    .min(0);
  dash.withPanel(requestRatePanel);

  // Row 2: Request Metrics
  dash.withRow(
    new RowBuilder("Request Metrics").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Request Rate by API Type",
    `sum(rate(llm_requests_total{${STAGE_FILTER}}[$__rate_interval])) by (api_type)`,
    {
      panelCustomization: (x) =>
        x.unit("reqps").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{api_type}}"),
    }
  );

  addTimeseriesChart(
    "Request Rate by Status",
    `sum(rate(llm_requests_total{${STAGE_FILTER}}[$__rate_interval])) by (status)`,
    {
      panelCustomization: (x) =>
        x.unit("reqps").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{status}}"),
    }
  );

  // Row 3: Token Usage
  dash.withRow(
    new RowBuilder("Token Usage").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Token Rate by Type",
    `sum(rate(llm_tokens_total{${FULL_FILTER}}[$__rate_interval])) by (token_type)`,
    {
      panelCustomization: (x) =>
        x.unit("short").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{token_type}}"),
    }
  );

  addTimeseriesChart(
    "Token Rate by Model",
    `sum(rate(llm_tokens_total{${FULL_FILTER}}[$__rate_interval])) by (model)`,
    {
      panelCustomization: (x) =>
        x.unit("short").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{model}}"),
    }
  );

  // Row 4: Cost Tracking
  dash.withRow(
    new RowBuilder("Cost Tracking").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Cost Rate by Model",
    `sum(rate(llm_cost_usd_total{${FULL_FILTER}}[$__rate_interval])) by (model) * 3600`,
    {
      panelCustomization: (x) =>
        x.unit("currencyUSD").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{model}} ($/hr)"),
    }
  );

  addTimeseriesChart(
    "Cost Rate by API Type",
    `sum(rate(llm_cost_usd_total{${FULL_FILTER}}[$__rate_interval])) by (api_type) * 3600`,
    {
      panelCustomization: (x) =>
        x.unit("currencyUSD").min(0),
      gridPos: { w: 12, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{api_type}} ($/hr)"),
    }
  );

  // Row 5: Latency
  dash.withRow(
    new RowBuilder("Latency").gridPos(gp({ w: 24, h: 1 }))
  );

  // Latency percentiles panel
  const latencyPanel = new TimeseriesBuilder()
    .title("Request Latency Percentiles")
    .unit("s")
    .min(0)
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.5, sum(rate(llm_request_duration_seconds_bucket{${FULL_FILTER}}[$__rate_interval])) by (le))`)
        .legendFormat("p50")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.9, sum(rate(llm_request_duration_seconds_bucket{${FULL_FILTER}}[$__rate_interval])) by (le))`)
        .legendFormat("p90")
    )
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.99, sum(rate(llm_request_duration_seconds_bucket{${FULL_FILTER}}[$__rate_interval])) by (le))`)
        .legendFormat("p99")
    );
  dash.withPanel(latencyPanel);

  // Latency by model
  const latencyByModelPanel = new TimeseriesBuilder()
    .title("P90 Latency by Model")
    .unit("s")
    .min(0)
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`histogram_quantile(0.9, sum(rate(llm_request_duration_seconds_bucket{${FULL_FILTER}}[$__rate_interval])) by (le, model))`)
        .legendFormat("{{model}}")
    );
  dash.withPanel(latencyByModelPanel);

  // Row 6: Anthropic Rate Limits
  dash.withRow(
    new RowBuilder("Anthropic Rate Limits").gridPos(gp({ w: 24, h: 1 }))
  );

  addTimeseriesChart(
    "Rate Limit Remaining",
    `anthropic_rate_limit_remaining{${STAGE_FILTER}}`,
    {
      panelCustomization: (x) =>
        x.min(0),
      gridPos: { w: 24, h: 8 },
      queryCustomization: (q) => q.legendFormat("{{type}}"),
    }
  );

  // Row 7: Per-User Breakdown
  dash.withRow(
    new RowBuilder("Per-User Breakdown").gridPos(gp({ w: 24, h: 1 }))
  );

  // Top users by cost (table)
  const topUsersCostTable = new TableBuilder()
    .title("Top 10 Users by Cost (24h)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(increase(llm_cost_usd_total{${STAGE_FILTER}}[24h])) by (user_id))`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topUsersCostTable);

  // Top users by tokens (table)
  const topUsersTokensTable = new TableBuilder()
    .title("Top 10 Users by Tokens (24h)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(increase(llm_tokens_total{${STAGE_FILTER}}[24h])) by (user_id))`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topUsersTokensTable);

  // Row 8: Per-VM Breakdown
  dash.withRow(
    new RowBuilder("Per-VM Breakdown").gridPos(gp({ w: 24, h: 1 }))
  );

  // Top VMs by cost (table)
  const topVMsCostTable = new TableBuilder()
    .title("Top 10 VMs by Cost (24h)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(increase(llm_cost_usd_total{${STAGE_FILTER}}[24h])) by (vm_name))`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topVMsCostTable);

  // Top VMs by tokens (table)
  const topVMsTokensTable = new TableBuilder()
    .title("Top 10 VMs by Tokens (24h)")
    .gridPos(gp({ w: 12, h: 8 }))
    .withTarget(
      new DataqueryBuilder()
        .expr(`topk(10, sum(increase(llm_tokens_total{${STAGE_FILTER}}[24h])) by (vm_name))`)
        .instant()
        .format(PromQueryFormat.Table)
    );
  dash.withPanel(topVMsTokensTable);

  return dash;
}

async function main() {
  if (TOKEN === undefined) {
    console.error(
      "Please provide a Grafana bearer token in the GRAFANA_BEARER_TOKEN environment variable."
    );
    process.exit(1);
  }
  if (GRAFANA_URL === undefined) {
    console.error(
      "Please provide the Grafana URL in the GRAFANA_URL environment variable."
    );
    process.exit(1);
  }
  await createDashboard(makeDevExeDashboard());
  await createDashboard(makeExeDevVMsDashboard());
  await createDashboard(makeGrpcMetricsDashboard());
  await createDashboard(makeGrafanaDashboard());
  await createDashboard(makeMonMonDashboard());
  await createDashboard(makeAwsCloudWatchDashboard());
  await createDashboard(makeHostsDashboard());
  await createDashboard(makeLLMGatewayDashboard());

  // Create alerts after dashboards are created
  await createAlerts();
}
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main();
}
