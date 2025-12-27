# exe.dev Logging Infrastructure

In Go, we use `slog` as our logging library.

## How the bits flow ... Architecture

We point our daemons to an otel-collector
running on mon, which muxes the logs to
S3, the fileysstem, and Honeycomb.


```
┌─────────────────┐     ┌─────────────────┐
│  exed-staging   │     │  exed-02 (prod) │
│  exelet-staging │     │  exelet-02/03/04│
└────────┬────────┘     └────────┬────────┘
         │ OTLP                  │ OTLP
         │ (deployment.env=      │ (deployment.env=
         │  staging)             │  production)
         └──────────┬────────────┘
                    ▼
            ┌───────────────┐
            │  mon:4318     │
            │  otel-collector│
            └───────┬───────┘
                    │
       ┌────────────┼────────────┐
       ▼            ▼            ▼
┌─────────────┐ ┌────────┐ ┌─────────────────────────────┐
│  Honeycomb  │ │ Files  │ │ S3: exe.dev-logs            │
│  (staging/  │ │ /var/  │ │ staging/year=.../hour=.../  │
│   prod key) │ │ log/   │ │ production/year=.../hour=../│
└─────────────┘ └────────┘ └─────────────────────────────┘
```

S3 is at: `s3://exe.dev-logs/` (us-west-2)
Local files are at: `/var/log/otel/{staging,production}/` on mon

## Querying Logs

### 1. Honeycomb

https://ui.honeycomb.io/bold-00/environments/production

### 2. DuckDB (S3)

```bash
./logs-duckdb.sh staging      # or production
```

There's some TODO here about not querying too much data,
caching, and so on.

## Service Configuration

Services send logs via OTLP with these environment variables (in
`/etc/default/exed` or `/etc/default/exelet`):

```bash
OTEL_SERVICE_NAME="exed"  # or exelet
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_ENDPOINT="http://mon:4318"
OTEL_RESOURCE_ATTRIBUTES="deployment.environment=staging"  # or production
```

## Collector Management

### Deploy/Update Collector

```bash
HONEYCOMB_API_KEY_STAGING=xxx HONEYCOMB_API_KEY_PRODUCTION=yyy ./deploy-otel-collector.sh
```

### Config Files on mon

- `/etc/otel-collector/config.yml` - collector config
- `/etc/default/otel-collector` - Honeycomb API keys

### Check Status

```bash
ssh ubuntu@mon "sudo systemctl status otel-collector"
ssh ubuntu@mon "sudo journalctl -fu otel-collector"
curl http://mon:13133/  # health check
```

## S3 Structure

Hive-style partitioning for DuckDB compatibility:

```
s3://exe.dev-logs/
├── staging/year=2025/month=12/day=27/hour=03/logs_*.json
├── production/year=2025/month=12/day=27/hour=03/logs_*.json
└── unknown/...
```

90-day lifecycle expiration policy.

## Relevant files in this Directory

- `otel-collector-config.yml` - collector configuration (source of truth)
- `otel-collector.service` - systemd unit file
- `deploy-otel-collector.sh` - deployment script
- `setup-mon-yace-policy.sh` - IAM/S3 setup (also creates the bucket)
- `logs-duckdb.sh` - DuckDB query helper
