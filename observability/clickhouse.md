# ClickHouse Cloud

See `clickhouse/clickhouse.md` for full documentation.

## Clusters

| Name | Host | Status |
|------|------|--------|
| Observability (new) | `tumy84t4c1.us-west-2.aws.clickhouse.cloud:8443` | Active |
| Original | `mjb7vf855d.us-west-2.aws.clickhouse.cloud:8443` | Deprecated 2026-03-31 |

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

## Prod Database Snapshots

In addition to logs and metrics, `exechsync` copies a small subset of exed's
SQLite production data into the `prod` database once per day. Tables are
tagged with `extract_date` and use `ReplacingMergeTree`; each has a
`*_latest` view that returns only the most recent snapshot — prefer those
for ad-hoc queries.

Tables: `prod.users`, `prod.teams`, `prod.team_members`, `prod.accounts`,
`prod.account_plans`, `prod.boxes` (plus matching `*_latest` views).

```sql
SELECT region, count() FROM prod.boxes_latest
WHERE status = 'running' GROUP BY region FORMAT PrettyCompact
```

See `clickhouse/clickhouse.md` for the full schema and more examples.

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
