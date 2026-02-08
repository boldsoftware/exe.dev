# metricsd - VM Metrics Storage Daemon

<human>
metricsd receives and stores per-VM metrics from exelets. It will
likely receive per-user metrics as well.

It uses DuckDB under the hood.

The motivation here is 3-fold:

1. Prometheus is falling over under the load of how
many VMs we have. The Prometheus alternatives largely look
terrifyingly complex.

2. I want to use SQL to query this stuff; PromQL is miserable,
and all Prometheus exporters look like the medicine is worse
than the disease. Ultimately, I want to understand the patterns
that these VMs are going through, and also possibly charge our
users for disk space based on real data.

3. I considered ClickHouse, which is supposed to be good for this
stuff. But there's real joy in not having a weird dependency that
everyone needs to figure out how to run locally. DuckDB, like
sqlite, is sufficiently embedded that it fits into our Go
ecosystem nicely enough. (The Go libraries for duckdb do some
trickery that I don't fully understand to avoid CGO; we shall see.)

At the moment, this is purely optional, and only started if you
start exed with -start-metricsd, or you set it up manually. This will
no doubt get more complicated.

</human>


## Architecture

```
┌─────────────┐     POST /write      ┌──────────────┐
│   exelet    │  ─────────────────>  │   metricsd   │
│  (per host) │   JSON MetricsBatch  │   (central)  │
└─────────────┘                      └──────┬───────┘
      │                                     │
      │ collects from VMs:                  │ stores in:
      │ - CPU (cgroup)                      │ - DuckDB
      │ - Memory RSS/Swap (/proc)           │
      │ - Disk (ZFS volsize/used)           │
      │ - Network (cgroup)                  │
      │                                     │
      └─────────────────────────────────────┘
```

## Running

```bash
# Development
go run ./cmd/metricsd -addr :8090 -db /tmp/metrics.duckdb -stage dev

# Production
metricsd -addr :8090 -db /data/metrics.duckdb -stage prod
```

### Flags

- `-addr` - HTTP listen address (default `:8090`)
- `-db` - Path to DuckDB database file (required)
- `-stage` - Environment stage: dev, staging, prod (required)

## HTTP Endpoints

### POST /write

Receive metrics from exelets. Accepts JSON batch:

```json
{
  "metrics": [
    {
      "timestamp": "2024-01-15T10:30:00Z",
      "vm_name": "e1e-abc1-0001-myvm",
      "cpu_usage_seconds": 125.5,
      "memory_rss_bytes": 1073741824,
      "memory_swap_bytes": 0,
      "disk_size_bytes": 10737418240,
      "disk_used_bytes": 2147483648,
      "disk_logical_used_bytes": 4294967296,
      "network_rx_bytes": 1048576,
      "network_tx_bytes": 524288,
      "nominal_cpus": 2.0,
      "nominal_memory_bytes": 4294967296
    }
  ]
}
```

### GET /query

Query stored metrics. Parameters:

- `vm_name` - Filter by VM name (optional)
- `limit` - Max results (default 100)

Returns same JSON format as /write.

### GET /metrics

Prometheus metrics endpoint. Exports:

- `metricsd_uptime_seconds` - Daemon uptime
- `metricsd_rows_inserted_total` - Total rows inserted
- `metricsd_insert_batch_duration_seconds` - Batch insert latency histogram
- `metricsd_insert_row_duration_seconds` - Per-row insert latency histogram

### GET /health

Health check endpoint. Returns 200 OK when healthy.

### GET /debug/pprof/*

Standard Go pprof debug endpoints.

## Exelet Configuration

Exelets send metrics to metricsd when configured:

```bash
exeletd \
  --metrics-daemon-url http://metricsd:8090 \
  --metrics-daemon-interval 10m \
  ...
```

The exelet:
- Collects per-VM metrics every 5 seconds (resource manager interval)
- Sends batched metrics to metricsd at the configured interval (default 10 min)
- Uses jitter on startup to avoid thundering herd across hosts

## Metrics Collected

| Metric | Source | Description |
|--------|--------|-------------|
| cpu_usage_seconds | cgroup cpu.stat | Total CPU time consumed |
| memory_rss_bytes | cgroup memory.current | Resident set size |
| memory_swap_bytes | /proc/\$pid/status | Swap usage for VM process |
| disk_size_bytes | ZFS volsize | Provisioned disk size |
| disk_used_bytes | ZFS used | Actual bytes on disk (compressed) |
| disk_logical_used_bytes | ZFS logicalused | Logical bytes (uncompressed) |
| network_rx_bytes | cgroup | Network bytes received |
| network_tx_bytes | cgroup | Network bytes transmitted |
| nominal_cpus | VM config | Configured CPU count |
| nominal_memory_bytes | VM config | Configured memory limit |

## Storage

Uses DuckDB with the Appender API for efficient bulk inserts. Data is stored in
a single `vm_metrics` table with automatic timestamping.

The DuckDB file can be queried directly for ad-hoc analysis:

```bash
duckdb /data/metrics.duckdb "SELECT vm_name, avg(cpu_usage_seconds) FROM vm_metrics GROUP BY vm_name"
```

## Package Structure

```
metricsd/
├── README.md           # This file
├── schema.go           # DuckDB schema and type re-exports
├── server.go           # HTTP server and DuckDB operations
├── server_test.go      # Server tests
└── types/
    └── types.go        # Metric and MetricsBatch types (no DuckDB deps)
```

The `types` subpackage exists to allow importing metric types without pulling
in DuckDB dependencies (useful for exelet which only needs to serialize metrics).
