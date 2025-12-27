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
SET s3_region='us-west-2';

-- Create view for logs
CREATE OR REPLACE VIEW logs AS
SELECT
    make_timestamp(l.timeUnixNano::bigint // 1000) as time,
    l.severityText as severity,
    l.body.stringValue as message,
    l.attributes as log_attributes,
    r.resource.attributes as resource_attributes,
    s.scope.name as scope_name,
    year, month, day, hour
FROM read_json('s3://exe.dev-logs/${ENV}/**/*.json', hive_partitioning=true)
CROSS JOIN UNNEST(resourceLogs) as t(r)
CROSS JOIN UNNEST(r.scopeLogs) as t2(s)
CROSS JOIN UNNEST(s.logRecords) as t3(l);

SELECT '${ENV} logs ready. Try: SELECT time, severity, message FROM logs ORDER BY time DESC LIMIT 20;' as status;
"
