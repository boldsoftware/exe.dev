#!/usr/bin/env python3
"""
Collect per-VM memory (RSS + swap) from all exelets and produce
a self-contained HTML page with Vega-Lite visualizations.

Usage:
    python3 data/analyze-memory-usage.py              # collect data + generate HTML
    python3 data/analyze-memory-usage.py --cached      # reuse data/vm-memory-data.csv

The HTML is written to data/vm-memory-report.html.
"""

import csv
import io
import json
import os
import subprocess
import sys


DATA_CSV = os.path.join(os.path.dirname(__file__), "vm-memory-data.csv")
OUTPUT_HTML = os.path.join(os.path.dirname(__file__), "vm-memory-report.html")

COLLECT_SCRIPT = r'''
for pid in $(pgrep -f "cloud-hypervisor.*api-socket"); do
  rss=$(awk '/^VmRSS:/{print $2}' /proc/$pid/status 2>/dev/null)
  swap=$(awk '/^VmSwap:/{print $2}' /proc/$pid/status 2>/dev/null)
  vmname=$(tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null | grep -oP '/runtime/\K[^/]+')
  if [ -n "$rss" ]; then
    echo "HOST_PLACEHOLDER,$pid,${vmname:-unknown},$rss,$swap"
  fi
done
'''


def get_exelet_hosts():
    """Get the list of exelet hostnames via ssh exe.dev exelets --json."""
    result = subprocess.run(
        ["ssh", "exe.dev", "exelets", "--json"],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode != 0:
        print(f"Error getting exelets: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    data = json.loads(result.stdout)
    return [e["host"] for e in data["exelets"]]


def collect_from_host(host):
    """SSH into a host and collect per-VM RSS/swap data. Returns list of csv rows."""
    script = COLLECT_SCRIPT.replace("HOST_PLACEHOLDER", host)
    try:
        result = subprocess.run(
            ["ssh", f"ubuntu@{host}", "bash"],
            input=script, capture_output=True, text=True, timeout=120,
        )
        if result.returncode != 0:
            print(f"  Warning: {host} returned exit code {result.returncode}", file=sys.stderr)
        rows = []
        for line in result.stdout.strip().splitlines():
            parts = line.split(",")
            if len(parts) == 5:
                rows.append(parts)
        return rows
    except subprocess.TimeoutExpired:
        print(f"  Warning: {host} timed out", file=sys.stderr)
        return []


def collect_all():
    """Collect data from all exelets in parallel using subprocess."""
    hosts = get_exelet_hosts()
    print(f"Collecting from {len(hosts)} exelets...")

    # Launch all SSH commands in parallel
    procs = {}
    for host in hosts:
        script = COLLECT_SCRIPT.replace("HOST_PLACEHOLDER", host)
        p = subprocess.Popen(
            ["ssh", f"ubuntu@{host}", "bash"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True,
        )
        procs[host] = (p, script)

    # Collect results
    all_rows = []
    for host, (p, script) in procs.items():
        try:
            stdout, stderr = p.communicate(input=script, timeout=120)
        except subprocess.TimeoutExpired:
            p.kill()
            print(f"  Warning: {host} timed out", file=sys.stderr)
            continue
        for line in stdout.strip().splitlines():
            parts = line.split(",")
            if len(parts) == 5:
                all_rows.append(parts)
        print(f"  {host}: {sum(1 for r in all_rows if r[0] == host)} VMs")

    # Write CSV
    with open(DATA_CSV, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["host", "pid", "vm_name", "rss_kb", "swap_kb"])
        w.writerows(all_rows)

    print(f"Wrote {len(all_rows)} rows to {DATA_CSV}")
    return all_rows


def load_csv():
    """Load previously collected CSV data."""
    rows = []
    with open(DATA_CSV) as f:
        reader = csv.reader(f)
        header = next(reader)  # skip header
        for row in reader:
            rows.append(row)
    return rows


def rows_to_json(rows):
    """Convert raw rows to JSON-serializable list of dicts with MB values."""
    data = []
    for row in rows:
        # Support both old 4-column and new 5-column formats
        if len(row) == 5:
            host, pid, vm_name, rss_kb, swap_kb = row[0], row[1], row[2], int(row[3]), int(row[4])
        else:
            host, pid, vm_name, rss_kb, swap_kb = row[0], row[1], "unknown", int(row[2]), int(row[3])
        data.append({
            "host": host,
            "pid": int(pid),
            "vm_name": vm_name,
            "rss_mb": round(rss_kb / 1024, 1),
            "swap_mb": round(swap_kb / 1024, 1),
            "total_mb": round((rss_kb + swap_kb) / 1024, 1),
        })
    return data


def generate_html(data):
    """Generate a self-contained HTML file with embedded Vega-Lite charts."""
    data_json = json.dumps(data)

    total_vms = len(data)
    total_rss_gb = sum(d["rss_mb"] for d in data) / 1024
    total_swap_gb = sum(d["swap_mb"] for d in data) / 1024
    vms_with_swap = sum(1 for d in data if d["swap_mb"] > 1)
    hosts = sorted(set(d["host"] for d in data))

    summary_rows = ""
    for host in hosts:
        host_data = [d for d in data if d["host"] == host]
        h_rss = sum(d["rss_mb"] for d in host_data) / 1024
        h_swap = sum(d["swap_mb"] for d in host_data) / 1024
        h_count = len(host_data)
        summary_rows += f"<tr><td>{host}</td><td>{h_count}</td><td>{h_rss:.1f}</td><td>{h_swap:.1f}</td><td>{h_rss+h_swap:.1f}</td></tr>\n"

    # Assign RSS category to each data point for stacked histograms
    rss_cat_breaks = [0, 256, 512, 1024, 2048, 4096]
    def rss_category(val):
        for i in range(len(rss_cat_breaks) - 1):
            if val < rss_cat_breaks[i + 1]:
                return f"{rss_cat_breaks[i]}-{rss_cat_breaks[i+1]}"
        return f"{rss_cat_breaks[-1]}+"
    rss_cat_order = [f"{rss_cat_breaks[i]}-{rss_cat_breaks[i+1]}" for i in range(len(rss_cat_breaks)-1)] + [f"{rss_cat_breaks[-1]}+"]

    # Similarly, swap categories for the RSS histogram
    swap_cat_breaks = [0, 1, 256, 1024, 4096]
    def swap_category(val):
        for i in range(len(swap_cat_breaks) - 1):
            if val < swap_cat_breaks[i + 1]:
                return f"{swap_cat_breaks[i]}-{swap_cat_breaks[i+1]}"
        return f"{swap_cat_breaks[-1]}+"
    swap_cat_order = [f"{swap_cat_breaks[i]}-{swap_cat_breaks[i+1]}" for i in range(len(swap_cat_breaks)-1)] + [f"{swap_cat_breaks[-1]}+"]

    for d in data:
        d["rss_category"] = rss_category(d["rss_mb"])
        d["swap_category"] = swap_category(d["swap_mb"])
    data_json = json.dumps(data)

    # Build crosstab with 100MB buckets
    crosstab_step = 100
    max_rss = max(d["rss_mb"] for d in data)
    max_swap = max(d["swap_mb"] for d in data)
    rss_breaks = list(range(0, int(max_rss) + crosstab_step, crosstab_step))
    swap_breaks = list(range(0, int(max_swap) + crosstab_step, crosstab_step))

    ct_step = 100
    ct_rss_breaks = list(range(0, int(max_rss) + ct_step, ct_step))
    ct_swap_breaks = list(range(0, int(max_swap) + ct_step, ct_step))

    def bucket_label(breaks, val):
        for i in range(len(breaks) - 1):
            if val < breaks[i + 1]:
                return f"{breaks[i]}-{breaks[i+1]}"
        return f"{breaks[-1]}+"

    ct_rss_labels = [f"{ct_rss_breaks[i]}-{ct_rss_breaks[i+1]}" for i in range(len(ct_rss_breaks)-1)] + [f"{ct_rss_breaks[-1]}+"]
    ct_swap_labels = [f"{ct_swap_breaks[i]}-{ct_swap_breaks[i+1]}" for i in range(len(ct_swap_breaks)-1)] + [f"{ct_swap_breaks[-1]}+"]

    crosstab = dict()
    for rl in ct_rss_labels:
        crosstab[rl] = dict()
        for sl in ct_swap_labels:
            crosstab[rl][sl] = 0
    for d in data:
        rl = bucket_label(ct_rss_breaks, d["rss_mb"])
        sl = bucket_label(ct_swap_breaks, d["swap_mb"])
        crosstab[rl][sl] += 1

    crosstab_header = "<tr><th>RSS \\ Swap (MB)</th>" + "".join(f"<th>{s}</th>" for s in ct_swap_labels) + "<th>Total</th></tr>"
    crosstab_rows = ""
    for rl in ct_rss_labels:
        cells = "".join(f"<td>{crosstab[rl][sl] or ''}</td>" for sl in ct_swap_labels)
        row_total = sum(crosstab[rl].values())
        if row_total == 0:
            continue
        crosstab_rows += f"<tr><td><b>{rl}</b></td>{cells}<td><b>{row_total}</b></td></tr>\n"
    col_totals = "".join(f"<td><b>{sum(crosstab[rl][sl] for rl in ct_rss_labels) or ''}</b></td>" for sl in ct_swap_labels)
    crosstab_rows += f"<tr><td><b>Total</b></td>{col_totals}<td><b>{total_vms}</b></td></tr>"

    # Build Vega-Lite specs as Python dicts to avoid f-string brace pain
    swap_histogram_spec = json.dumps({
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        "width": 700, "height": 350,
        "data": {"values": "DATA_PLACEHOLDER"},
        "mark": "bar",
        "encoding": {
            "x": {"bin": {"maxbins": 50}, "field": "swap_mb", "title": "Swap (MB)", "type": "quantitative"},
            "y": {"aggregate": "count", "title": "Number of VMs"},
            "color": {
                "field": "rss_category", "type": "nominal", "title": "RSS (MB)",
                "sort": rss_cat_order,
                "scale": {"scheme": "viridis"},
            },
            "order": {"field": "rss_category", "sort": "ascending"},
            "tooltip": [
                {"bin": {"maxbins": 50}, "field": "swap_mb", "title": "Swap (MB)"},
                {"field": "rss_category", "title": "RSS category"},
                {"aggregate": "count", "title": "Count"},
            ],
        },
    }).replace('"DATA_PLACEHOLDER"', "data")

    rss_histogram_spec = json.dumps({
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        "width": 700, "height": 350,
        "data": {"values": "DATA_PLACEHOLDER"},
        "mark": "bar",
        "encoding": {
            "x": {"bin": {"maxbins": 50}, "field": "rss_mb", "title": "RSS (MB)", "type": "quantitative"},
            "y": {"aggregate": "count", "title": "Number of VMs"},
            "color": {
                "field": "swap_category", "type": "nominal", "title": "Swap (MB)",
                "sort": swap_cat_order,
                "scale": {"scheme": "magma"},
            },
            "order": {"field": "swap_category", "sort": "ascending"},
            "tooltip": [
                {"bin": {"maxbins": 50}, "field": "rss_mb", "title": "RSS (MB)"},
                {"field": "swap_category", "title": "Swap category"},
                {"aggregate": "count", "title": "Count"},
            ],
        },
    }).replace('"DATA_PLACEHOLDER"', "data")

    scatter_spec = json.dumps({
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        "width": 700, "height": 400,
        "data": {"values": "DATA_PLACEHOLDER"},
        "mark": {"type": "circle", "opacity": 0.5, "size": 30},
        "encoding": {
            "x": {"field": "rss_mb", "type": "quantitative", "title": "RSS (MB)", "scale": {"zero": True}},
            "y": {"field": "swap_mb", "type": "quantitative", "title": "Swap (MB)", "scale": {"zero": True}},
            "color": {"field": "host", "type": "nominal", "title": "Exelet"},
            "tooltip": [
                {"field": "vm_name", "title": "VM"},
                {"field": "host", "title": "Host"},
                {"field": "rss_mb", "title": "RSS (MB)", "format": ",.0f"},
                {"field": "swap_mb", "title": "Swap (MB)", "format": ",.0f"},
                {"field": "total_mb", "title": "Total (MB)", "format": ",.0f"},
            ],
        },
    }).replace('"DATA_PLACEHOLDER"', "data")

    heatmap_spec = json.dumps({
        "$schema": "https://vega.github.io/schema/vega-lite/v5.json",
        "width": 700, "height": 500,
        "data": {"values": "DATA_PLACEHOLDER"},
        "mark": "rect",
        "encoding": {
            "x": {"bin": {"step": 100}, "field": "rss_mb", "type": "quantitative", "title": "RSS (MB)"},
            "y": {"bin": {"step": 100}, "field": "swap_mb", "type": "quantitative", "title": "Swap (MB)"},
            "color": {
                "aggregate": "count", "type": "quantitative", "title": "VM Count",
                "scale": {"scheme": "inferno"},
            },
            "tooltip": [
                {"bin": {"step": 100}, "field": "rss_mb", "title": "RSS (MB)"},
                {"bin": {"step": 100}, "field": "swap_mb", "title": "Swap (MB)"},
                {"aggregate": "count", "title": "Count"},
            ],
        },
    }).replace('"DATA_PLACEHOLDER"', "data")

    html = f"""<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>VM Memory Usage Report</title>
<script src="https://cdn.jsdelivr.net/npm/vega@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-lite@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-embed@6"></script>
<style>
  body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background: #fafafa; }}
  h1 {{ border-bottom: 2px solid #333; padding-bottom: 8px; }}
  h2 {{ margin-top: 40px; }}
  .summary {{ display: flex; gap: 20px; flex-wrap: wrap; margin: 20px 0; }}
  .stat {{ background: white; border: 1px solid #ddd; border-radius: 8px; padding: 16px 24px; min-width: 140px; }}
  .stat .value {{ font-size: 28px; font-weight: bold; }}
  .stat .label {{ color: #666; font-size: 13px; margin-top: 4px; }}
  .chart {{ background: white; border: 1px solid #ddd; border-radius: 8px; padding: 16px; margin: 20px 0; }}
  table {{ border-collapse: collapse; width: 100%; background: white; }}
  th, td {{ padding: 6px 12px; text-align: right; border: 1px solid #ddd; font-size: 13px; }}
  th {{ background: #f0f0f0; }}
  td:first-child, th:first-child {{ text-align: left; }}
  .crosstab {{ overflow-x: auto; }}
  .crosstab td:empty {{ background: #f8f8f8; }}
</style>
</head>
<body>
<h1>VM Memory Usage Report</h1>

<div class="summary">
  <div class="stat"><div class="value">{total_vms:,}</div><div class="label">Total VMs</div></div>
  <div class="stat"><div class="value">{total_rss_gb:.0f} GB</div><div class="label">Total RSS</div></div>
  <div class="stat"><div class="value">{total_swap_gb:.0f} GB</div><div class="label">Total Swap</div></div>
  <div class="stat"><div class="value">{total_rss_gb+total_swap_gb:.0f} GB</div><div class="label">Total Committed</div></div>
  <div class="stat"><div class="value">{vms_with_swap:,}</div><div class="label">VMs with &gt;1MB Swap</div></div>
</div>

<h2>Swap Usage Histogram (colored by RSS)</h2>
<div class="chart" id="swap-histogram"></div>

<h2>RSS Usage Histogram (colored by Swap)</h2>
<div class="chart" id="rss-histogram"></div>

<h2>RSS vs Swap (scatter)</h2>
<div class="chart" id="rss-swap-scatter"></div>

<h2>RSS vs Swap Heatmap (100 MB buckets)</h2>
<div class="chart" id="rss-swap-heatmap"></div>

<h2>RSS vs Swap Crosstab (VM count, 500 MB buckets)</h2>
<div class="crosstab">
<table>
{crosstab_header}
{crosstab_rows}
</table>
</div>

<h2>Per-Host Summary</h2>
<table>
<tr><th>Host</th><th>VMs</th><th>RSS (GB)</th><th>Swap (GB)</th><th>Total (GB)</th></tr>
{summary_rows}
<tr style="font-weight:bold"><td>Total</td><td>{total_vms}</td><td>{total_rss_gb:.1f}</td><td>{total_swap_gb:.1f}</td><td>{total_rss_gb+total_swap_gb:.1f}</td></tr>
</table>

<script>
const data = {data_json};
vegaEmbed('#swap-histogram', {swap_histogram_spec}, {{actions: false}});
vegaEmbed('#rss-histogram', {rss_histogram_spec}, {{actions: false}});
vegaEmbed('#rss-swap-scatter', {scatter_spec}, {{actions: false}});
vegaEmbed('#rss-swap-heatmap', {heatmap_spec}, {{actions: false}});
</script>
</body>
</html>"""

    with open(OUTPUT_HTML, "w") as f:
        f.write(html)
    print(f"Wrote {OUTPUT_HTML}")


def main():
    cached = "--cached" in sys.argv

    if cached:
        if not os.path.exists(DATA_CSV):
            print(f"Error: {DATA_CSV} not found. Run without --cached first.", file=sys.stderr)
            sys.exit(1)
        print(f"Using cached data from {DATA_CSV}")
        rows = load_csv()
    else:
        rows = collect_all()

    print(f"Loaded {len(rows)} VM records")
    data = rows_to_json(rows)
    generate_html(data)


if __name__ == "__main__":
    main()
