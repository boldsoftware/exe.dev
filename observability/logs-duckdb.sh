#!/bin/bash
set -euo pipefail

# Launch DuckDB with AWS credentials and pre-configured views for exe.dev logs
# Usage: ./logs-duckdb.sh [staging|production]

ENV="${1:-staging}"

if [[ "$ENV" != "staging" && "$ENV" != "production" ]]; then
    echo "Usage: $0 [staging|production]" >&2
    exit 1
fi

# Export AWS credentials for DuckDB
eval "$(aws configure export-credentials --format env 2>/dev/null)" || {
    echo "Failed to export AWS credentials. Run 'aws sso login' first." >&2
    exit 1
}

exec duckdb -cmd "
INSTALL aws;
LOAD aws;
INSTALL httpfs;
LOAD httpfs;
SET s3_region='us-west-2';
SET enable_http_metadata_cache=true;

-- Create view for logs (use explicit JSON types to handle polymorphic OTLP attribute values)
CREATE OR REPLACE VIEW logs AS
SELECT
    make_timestamp(json_extract_string(l, '\$.timeUnixNano')::bigint // 1000) as time,
    json_extract_string(l, '\$.severityText') as severity,
    json_extract_string(l, '\$.body.stringValue') as message,
    json_extract(l, '\$.attributes') as log_attributes,
    json_extract(r, '\$.resource.attributes') as resource_attributes,
    json_extract_string(s, '\$.scope.name') as scope_name,
    year, month, day, hour
FROM read_json('s3://exe.dev-logs/${ENV}/**/*.json', hive_partitioning=true, auto_detect=false, columns={resourceLogs: 'JSON[]'})
CROSS JOIN UNNEST(resourceLogs) as t(r)
CROSS JOIN UNNEST(CAST(json_extract(r, '\$.scopeLogs') AS JSON[])) as t2(s)
CROSS JOIN UNNEST(CAST(json_extract(s, '\$.logRecords') AS JSON[])) as t3(l);

SELECT '${ENV} logs ready. Try: SELECT time, severity, message FROM logs ORDER BY time DESC LIMIT 20;' as status;
"
