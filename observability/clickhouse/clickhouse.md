# ClickHouse Cloud

Host: `mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443` (HTTPS)

## Users

| User | Purpose | Password env var |
|------|---------|-----------------|
| `default` | Admin | `CLICKHOUSE_PASSWORD` |
| `readonly` | Read-only queries (SELECT only, `readonly = 1`) | `CLICKHOUSE_READONLY_PASSWORD` |

## Creating a new read-only user

Generate a password:

```bash
openssl rand -base64 18
```

Connect as admin and create the user:

```bash
curl --user "default:$CLICKHOUSE_PASSWORD" \
  --data-binary "CREATE USER IF NOT EXISTS <username> IDENTIFIED WITH sha256_password BY '<password>' SETTINGS readonly = 1" \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443

curl --user "default:$CLICKHOUSE_PASSWORD" \
  --data-binary "GRANT CURRENT GRANTS(SELECT ON *.*) TO <username>" \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443
```

Note: `GRANT CURRENT GRANTS(...)` is used instead of plain `GRANT` because the
`default` user lacks GRANT OPTION on some system tables (e.g. `system.zookeeper`).

## Querying

```bash
curl --user "readonly:$CLICKHOUSE_READONLY_PASSWORD" \
  --data-binary 'SELECT version()' \
  https://mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443
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

### Tips

- Always use `FORMAT PrettyCompact` for readable curl output
- `Timestamp >= today()` filters to current UTC day
- The `remaining_credit_usd` decreases over time; the latest value is the most recent balance
- Subquery approach works well to alias the verbose `LogAttributes['...']` keys

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

| Resource | ID |
|----------|------|
| Organization | `76e3b458-f59b-4d98-a1c6-f45b8d87f6ec` |
| Service | `f7e2b7ae-7e5f-4339-bd4c-d076773ac9bc` |
| Metrics source | `69a63f8fb6b5862ebca57245` |
| Logs source | `69a63f8fb6b5862ebca5723d` |

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
          "valueExpression": "<metric-name>",
          "metricType": "<gauge|sum|histogram|summary>",
          "aggFn": "<aggregation>",
          "where": "",
          "whereLanguage": "lucene"
        }],
        "groupBy": "<resource-or-metric-attribute-key>"
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

**`valueExpression`**: The OTel metric name (e.g. `node_pressure_cpu_waiting_seconds_total`).
Required for all aggFn except `count`.

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

**Status (2026-03-14)**: Despite `metricType` being preserved in the API response, metrics
dashboard tiles still fail to render. The managed ClickStack API may have a bug where
`metricType` is stored but not passed through to the chart rendering pipeline. This needs
to be revisited — try creating a dashboard tile manually in the Clickstack UI first, then
GET it via the API to see what the working tile config looks like. That will reveal the
exact field names and values the UI uses.

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

### Debugging approach for next attempt

1. Create a metrics tile manually in the Clickstack web UI (pick any metric, e.g.
   `node_load1` which is a gauge).
2. GET that dashboard via the API and inspect the exact tile config.
3. Compare the working config against what the API produces when you POST/PUT.
4. The diff will reveal any fields the UI sets that the API doesn't expose.

### Host PSI Metrics (in `otel_metrics_sum`)

| Metric | PSI category |
|--------|-------------|
| `node_pressure_cpu_waiting_seconds_total` | CPU some |
| `node_pressure_io_waiting_seconds_total` | IO some |
| `node_pressure_io_stalled_seconds_total` | IO full |
| `node_pressure_memory_waiting_seconds_total` | Memory some |
| `node_pressure_memory_stalled_seconds_total` | Memory full |

Grouped by `service.instance.id` (e.g. `exe-ctr-07:9100`, `exed-02:19100`).

### Existing Dashboards

| Name | ID | Description |
|------|-----|-------------|
| Host Resource Pressure (PSI) | `69b5c43825fefba28dfaef6c` | 5 panels (currently broken — metricType issue) |
