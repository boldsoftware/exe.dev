# ClickHouse Cloud

## Clusters

| Name | Host | Status |
|------|------|--------|
| Observability (new) | `tumy84t4c1.us-west-2.aws.clickhouse.cloud:8443` | Active |
| Original | `mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443` | Deprecated 2026-03-31 |

## Users (observability cluster)

| User | Purpose | Password env var |
|------|---------|-----------------|
| `otel-ingest` | Collector ingestion (SELECT/INSERT/CREATE/ALTER/DROP TABLE) | `CLICKHOUSE_OBSERVABILITY_PASSWORD` |
| `philip-rw` | Read-write for ad-hoc use | `CLICKHOUSE_RW` |
| `philip-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `josh-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `bryan-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `ian-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `evan-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `david-ro` | Read-only | See `clickhouse-ro-users.txt` |
| `shaun-ro` | Read-only | See `clickhouse-ro-users.txt` |

## Querying

Use your `*-ro` user and password from `clickhouse-ro-users.txt`:

```bash
curl --user "$CLICKHOUSE_USER:$CLICKHOUSE_PASSWORD" \
  --data-binary 'SELECT version()' \
  https://tumy84t4c1.us-west-2.aws.clickhouse.cloud:8443
```

Set `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD` from your credentials in `clickhouse-ro-users.txt`:

```bash
export CLICKHOUSE_USER="yourname-ro"
export CLICKHOUSE_PASSWORD="yourpassword"
```

## OTel Logs Schema

**Table**: `otel_logs`

Key columns: `Timestamp`, `SeverityText`, `Body`, `LogAttributes` (Map(String, String)).

All structured fields live inside `LogAttributes` as string values — access via `LogAttributes['key']`.

### Important LogAttributes keys

| Key | Type | Description |
|-----|------|-------------|
| `cost_usd` | string (cast to Float64) | Per-request LLM cost |
| `user_id` | string | e.g. `usrSM27RI7TOCZF3` |
| `llm_model` | string | e.g. `claude-opus-4-6` |
| `conversation_id` | string | Groups requests into sessions |
| `input_tokens` | string (cast to UInt64) | Non-cache input tokens |
| `output_tokens` | string (cast to UInt64) | Output tokens |
| `cache_creation_tokens` | string (cast to UInt64) | Prompt cache write tokens |
| `cache_read_tokens` | string (cast to UInt64) | Prompt cache read tokens |
| `remaining_credit_usd` | string | User's remaining balance after request |
| `vm_name` | string | VM that originated the request |
| `host` | string | exed host that handled the request |
| `shelley_version` | string | Client version hash |
| `request_type` | string | e.g. `gateway` |
| `log_type` | string | e.g. `http_request` |
| `uri` | string | e.g. `/v1/messages` |
| `trace_id` | string | Distributed trace ID |
| `proxy` | string | `true` for proxied requests (exeprox) |
| `vm_id` | string | VM database ID |
| `vm_owner_user_id` | string | User ID of VM owner |
| `exelet_host` | string | e.g. `tcp://exelet-lax-prod-04:9080` |
| `route_port` | string | Container port being proxied to |
| `route_share` | string | `public` or `private` |
| `local_addr` | string | Local address that received the request |
| `socket_rtt_us` | string (cast to UInt64) | TCP socket RTT in microseconds (TLS HTTP requests, SSH connections) |
| `grpc.service` | string | gRPC service name (e.g. `exe.proxy.v1.ProxyInfoService`) |
| `grpc.method` | string | gRPC method (e.g. `BoxInfo`, `TopLevelCert`, `UserInfo`) |
| `grpc.code` | string | gRPC status code (e.g. `OK`) |
| `grpc.time_ms` | string (cast to Float64) | gRPC call duration in milliseconds |
| `grpc.component` | string | `client` or `server` |

### Query patterns

Filter for cost-bearing requests:

```sql
WHERE LogAttributes['cost_usd'] != ''
```

Cast string values for aggregation:

```sql
toFloat64(LogAttributes['cost_usd'])
toUInt64(LogAttributes['input_tokens'])
```

Today's cost by model for a user:

```sql
SELECT
    LogAttributes['llm_model'] AS llm_model,
    count() AS requests,
    round(sum(toFloat64(LogAttributes['cost_usd'])), 4) AS total_cost_usd
FROM otel_logs
WHERE LogAttributes['cost_usd'] != ''
  AND LogAttributes['user_id'] = 'USER_ID'
  AND Timestamp >= today()
GROUP BY llm_model
ORDER BY total_cost_usd DESC
FORMAT PrettyCompact
```

Cost by conversation:

```sql
SELECT
    LogAttributes['conversation_id'] AS conversation_id,
    count() AS requests,
    round(sum(toFloat64(LogAttributes['cost_usd'])), 4) AS cost_usd,
    min(Timestamp) AS first_request,
    max(Timestamp) AS last_request
FROM otel_logs
WHERE LogAttributes['cost_usd'] != ''
  AND LogAttributes['user_id'] = 'USER_ID'
  AND Timestamp >= today()
GROUP BY conversation_id
ORDER BY cost_usd DESC
FORMAT PrettyCompact
```

Latency leaderboard — users with highest median RTT (last 24h):

```sql
SELECT
    LogAttributes['user_id'] AS user_id,
    count() AS requests,
    round(quantile(0.5)(toUInt64(LogAttributes['socket_rtt_us']))) AS p50_rtt_us,
    round(quantile(0.95)(toUInt64(LogAttributes['socket_rtt_us']))) AS p95_rtt_us,
    round(max(toUInt64(LogAttributes['socket_rtt_us']))) AS max_rtt_us
FROM otel_logs
WHERE LogAttributes['socket_rtt_us'] != ''
  AND LogAttributes['user_id'] != ''
  AND Timestamp >= now() - INTERVAL 24 HOUR
GROUP BY user_id
HAVING requests >= 5
ORDER BY p50_rtt_us DESC
LIMIT 50
FORMAT PrettyCompact
```

RTT by exeprox edge (to see which regions have slow users):

```sql
SELECT
    ResourceAttributes['host.name'] AS edge,
    count() AS requests,
    round(quantile(0.5)(toUInt64(LogAttributes['socket_rtt_us']))) AS p50_rtt_us,
    round(quantile(0.95)(toUInt64(LogAttributes['socket_rtt_us']))) AS p95_rtt_us
FROM otel_logs
WHERE LogAttributes['socket_rtt_us'] != ''
  AND ServiceName = 'exeprox'
  AND Timestamp >= now() - INTERVAL 24 HOUR
GROUP BY edge
ORDER BY p50_rtt_us DESC
FORMAT PrettyCompact
```

SSH connection RTT by user (from sshpiperd via exed canonical log lines):

```sql
SELECT
    LogAttributes['user_id'] AS user_id,
    LogAttributes['username'] AS ssh_user,
    count() AS connections,
    round(quantile(0.5)(toUInt64(LogAttributes['socket_rtt_us']))) AS p50_rtt_us,
    round(quantile(0.95)(toUInt64(LogAttributes['socket_rtt_us']))) AS p95_rtt_us
FROM otel_logs
WHERE LogAttributes['log_type'] IN ('ssh_proxy_auth', 'vm-ssh-connection')
  AND LogAttributes['socket_rtt_us'] != ''
  AND Timestamp >= now() - INTERVAL 24 HOUR
GROUP BY user_id, ssh_user
ORDER BY p50_rtt_us DESC
FORMAT PrettyCompact
```

### Tips

- Always use `FORMAT PrettyCompact` for readable curl output
- `Timestamp >= today()` filters to current UTC day
- The `remaining_credit_usd` decreases over time; the latest value is the most recent balance
- Subquery approach works well to alias the verbose `LogAttributes['...']` keys

## Querying from an exe.dev VM

```bash
curl -s --user "$CLICKHOUSE_USER" \
  --data-binary 'SELECT version()' \
  https://tumy84t4c1.us-west-2.aws.clickhouse.cloud:8443
```

## Services and Hosts

`ServiceName` identifies the emitting binary. `ResourceAttributes['host.name']` identifies
the specific host. `ResourceAttributes['deployment.environment']` is `production` or `staging`.

| ServiceName | What | Example hosts |
|-------------|------|---------------|
| `exeprox` | Edge proxy (HTTP/SSH) | `edge1-LAX`, `edge1-FRA`, `edge1-LHR`, etc. |
| `exed` | Central controller | `exed-02` |
| `exelet` | Container host agent | `exelet-lax-prod-04`, `exelet-lax2-prod-01` |
| `metricsd` | Metrics collector | `exed-02` |

## Request Tracing

All services propagate `trace_id` via context. For HTTP requests, exeprox generates
a trace_id in `tracing.HTTPMiddleware` and propagates it to exed via gRPC metadata.

To trace a request, find it by host/uri/timestamp, then use the trace_id that
exeprox assigned to follow the full call chain.

### Tracing a request end-to-end

1. Find the canonical log line for the request:

```sql
SELECT
    Timestamp,
    LogAttributes['trace_id'] AS trace_id,
    LogAttributes['host'] AS host,
    LogAttributes['uri'] AS uri,
    Body
FROM otel_logs
WHERE LogAttributes['host'] = 'blog.philz.dev'
  AND LogAttributes['uri'] = '/some-path'
  AND LogAttributes['log_type'] = 'http_request'
  AND Timestamp >= now() - INTERVAL 10 MINUTE
ORDER BY Timestamp DESC
LIMIT 5
FORMAT PrettyCompact
```

2. Fetch all events for that trace:

```sql
SELECT
    Timestamp,
    ServiceName,
    SeverityText,
    Body,
    LogAttributes['grpc.service'] AS grpc_service,
    LogAttributes['grpc.method'] AS grpc_method,
    LogAttributes['grpc.code'] AS grpc_code,
    LogAttributes['grpc.time_ms'] AS grpc_time_ms,
    LogAttributes['grpc.component'] AS grpc_component,
    LogAttributes['log_type'] AS log_type,
    ResourceAttributes['host.name'] AS hostname
FROM otel_logs
WHERE LogAttributes['trace_id'] = 'TRACE_ID_HERE'
ORDER BY Timestamp ASC
FORMAT PrettyCompact
```

### What a typical HTTP proxy trace looks like

For a request to a custom domain (e.g. `blog.philz.dev`), a cold trace has
~16 events across exeprox and exed:

1. exeprox: `fetching box info` → gRPC `BoxInfo` client call
2. exed: gRPC `BoxInfo` server handling (resolves hostname → VM)
3. exeprox: `fetching box info exists` → gRPC `TopLevelCert` client call
4. exed: gRPC `TopLevelCert` server handling (TLS cert for custom domain)
5. exeprox: `fetching user info` → gRPC `UserInfo` client call
6. exed: gRPC `UserInfo` server handling (resolves VM owner)
7. exeprox: HTTP proxy to blogd container (not instrumented)
8. exeprox: canonical `http_request` log line with all attributes

On warm requests, BoxInfo and UserInfo are cached in exeprox's in-memory
`hashtriemap`, reducing the trace to ~5 events (only TopLevelCert gRPC call
remains, since TLS handshakes are per-connection).

### Tracing gaps

- **exeprox → container**: HTTP over the SSH sshpool tunnel; not instrumented

### Latency from OTEL pipeline

Logs take 30-60 seconds to appear in ClickHouse after being emitted
(batching in OTEL collector + ClickHouse ingestion).

## Clickstack API (Dashboard Management)

Clickstack is ClickHouse's built-in observability UI (based on HyperDX). Dashboards can
be managed programmatically via the ClickHouse Cloud API.

### Authentication

Uses ClickHouse Cloud API keys (not the DB user/password). Env vars:

| Var | Purpose |
|-----|---------|
| `CLICKHOUSE_API_ID` | Cloud API key ID |
| `CLICKHOUSE_API_SECRET` | Cloud API key secret |

**Important**: `curl --user` doesn't work with these keys. You must construct the
Authorization header manually:

```bash
AUTH="Authorization: Basic $(echo -n "${CLICKHOUSE_API_ID}:${CLICKHOUSE_API_SECRET}" | base64)"
curl -s -H "$AUTH" "$URL"
```

### IDs

| Resource | Observability (new) | Original (deprecated) |
|----------|--------------------|-----------------------|
| Organization | `76e3b458-f59b-4d98-a1c6-f45b8d87f6ec` | (same) |
| Service | `3d718833-6a3d-46f4-90e3-329159975f64` | `f7e2b7ae-7e5f-4339-bd4c-d076773ac9bc` |
| Metrics source | `69cc2aaa2562ef5f27226375` | `69a63f8fb6b5862ebca57245` |
| Logs source | `69cc2aaa15bdab674da27769` | `69a63f8fb6b5862ebca5723d` |

These can be discovered via `GET /organizations` -> `GET /organizations/{orgId}/services`
-> `GET .../clickstack/sources`.

### Base URL

```
https://api.clickhouse.cloud/v1/organizations/{orgId}/services/{serviceId}/clickstack
```

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/sources` | List data sources (metrics, logs) |
| GET | `/dashboards` | List all dashboards |
| GET | `/dashboards/{id}` | Get a dashboard |
| POST | `/dashboards` | Create a dashboard |
| PUT | `/dashboards/{id}` | Update a dashboard (full replace) |
| DELETE | `/dashboards/{id}` | Delete a dashboard |

### Dashboard JSON Schema

```json
{
  "name": "Dashboard Name",
  "tiles": [
    {
      "x": 0, "y": 0, "w": 24, "h": 10,
      "name": "Tile Title",
      "config": {
        "displayType": "line",
        "sourceId": "<metrics-or-logs-source-id>",
        "select": [{
          "valueExpression": "Value",
          "metricName": "<metric-name>",
          "metricType": "<gauge|sum|histogram|summary>",
          "aggFn": "<aggregation>",
          "where": "",
          "whereLanguage": "lucene"
        }],
        "groupBy": "ResourceAttributes['service.instance.id']"
      }
    }
  ],
  "tags": ["optional", "tags"]
}
```

**Grid**: 24 units wide. `x` + `w` <= 24. `y` positions stack vertically.

**`aggFn` values**: `avg`, `count`, `count_distinct`, `last_value`, `max`, `min`, `quantile`, `sum`, `any`, `none`.
The open-source HyperDX API also supports rate variants (`avg_rate`, `sum_rate`, etc.)
but the managed ClickHouse Cloud API does not.

**`displayType` values**: `line` (confirmed working; others likely include `bar`, `area`, `number`).

**`valueExpression`**: The column to aggregate — use `"Value"` for metrics tiles.

**`metricName`**: The OTel metric name (e.g. `node_pressure_cpu_waiting_seconds_total`).
This is a separate field from `valueExpression` — the metric name filters which rows to
read, and `valueExpression` says which column to aggregate.

**`groupBy`**: A single string (not an array). Use OTel resource attribute keys like
`service.instance.id`, or metric attribute keys.

**Auto-added fields**: The API adds `asRatio: false` and `fillNulls: true` to tile configs.

### metricType field — CRITICAL for metrics tiles

Without `metricType`, metrics tiles fail at render time with:
`"no query support for metric type=undefined"`

This tells Clickstack which OTel table to query. Values are **lowercase**:
`gauge`, `sum`, `histogram`, `summary`, `exponential histogram`.

The field goes inside each `select[]` item as `metricType` (not `metricDataType`).

**Provenance**: Clickstack is built on HyperDX. In the open-source HyperDX external API
(v2), the field is called `metricDataType` and gets translated to `metricType` internally
(see `packages/api/src/utils/externalApi.ts`). The managed ClickHouse Cloud API appears to
use the internal field name `metricType` directly — it preserves `metricType` in responses
but silently strips `metricDataType`.

**Status (2026-03-31)**: `metricType` works correctly on the new observability cluster.
The issue on the old cluster may have been a version bug. Dashboard tiles created via API
with `metricType` render correctly.

### OTel Metrics Tables

Metrics are stored across four tables based on type:

| Table | metricType value | Description |
|-------|-----------------|-------------|
| `otel_metrics_gauge` | `gauge` | Point-in-time values |
| `otel_metrics_sum` | `sum` | Counters/cumulative sums |
| `otel_metrics_histogram` | `histogram` | Histograms |
| `otel_metrics_summary` | `summary` | Summaries |

Common columns: `MetricName`, `Value`, `TimeUnix`, `Attributes` (Map), `ResourceAttributes` (Map).

### API Gotchas

- The API **silently ignores unknown fields** — no validation error, just stripped from
  the stored config. If a field disappears from the GET response, the API doesn't
  recognize it.
- `metricType` IS preserved; `metricDataType` is silently stripped.
- `groupBy` is a single string, not an array. Passing an array gives a validation error.
- `aggFn: "rate"` and `aggFn: "sum_rate"` are rejected with validation errors listing the
  valid enum values.
- `curl --user "$ID:$SECRET"` fails with "Bad Authorization header" — must manually
  base64-encode and use `-H "Authorization: Basic ..."`.
- PUT on dashboards is a full replace (not a patch). You must send all tiles.

### Host PSI Metrics (in `otel_metrics_sum`)

| Metric | PSI category |
|--------|-------------|
| `node_pressure_cpu_waiting_seconds_total` | CPU some |
| `node_pressure_io_waiting_seconds_total` | IO some |
| `node_pressure_io_stalled_seconds_total` | IO full |
| `node_pressure_memory_waiting_seconds_total` | Memory some |
| `node_pressure_memory_stalled_seconds_total` | Memory full |

Grouped by `service.instance.id` (e.g. `exe-ctr-07:9100`, `exed-02:19100`).

### Existing Dashboards (observability cluster)

| Name | ID | Definition |
|------|-----|------------|
| Hosts | `69cc4919471d49fd0e076eea` | `hosts_dashboard()` in `sync-dashboards.py` |

Dashboards are defined in code in `clickhouse/dashboards/sync-dashboards.py`. To sync:

```bash
./clickhouse/dashboards/sync-dashboards.py          # sync all
./clickhouse/dashboards/sync-dashboards.py hosts     # sync one by name
./clickhouse/dashboards/sync-dashboards.py --dump    # print JSON without syncing
```

The script matches by name — existing dashboards are updated in place, new ones are created.
Add new dashboards by defining a function with the `@dashboard` decorator.

### Existing Dashboards (old cluster, deprecated)

| Name | ID | Description |
|------|-----|-------------|
| Host Resource Pressure (PSI) | `69b5c43825fefba28dfaef6c` | 5 panels (was broken — metricType issue) |
