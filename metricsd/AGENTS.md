# metricsd agent notes

## Running locally

```bash
go run ./cmd/metricsd -db /path/to/metrics.duckdb -port 8000 -stage local
```

The duckdb files in prod are on the exed host. For local dev, copy or use existing `.duckdb` files.

## Static files are embedded

The HTML/CSS/JS in `static/` are served via `embed.FS`. You must restart the server after changing them — the browser won't pick up changes from disk.

## Sparklines data characteristics

- Exelets send metrics every ~10 minutes. Over 24 hours, most VMs have only 2-4 data points.
- Derived metrics (CPU %, network rates) require deltas between consecutive rows, so a VM with N rows yields N-1 derived values. With 2 rows, you get 1 derived point — not enough to draw a line. Handle single-point data explicitly (e.g., draw a dot).
- CPU can exceed 100% of nominal (e.g., 3.20/2 CPUs). Don't clamp or assume it fits in 0-100%.
