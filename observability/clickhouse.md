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
