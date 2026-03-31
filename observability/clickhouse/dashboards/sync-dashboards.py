#!/usr/bin/env python3
"""
Sync dashboard definitions to ClickHouse Clickstack.

Dashboards are defined in code below. The script creates or updates them
via the Clickstack API, matching by name for idempotency.

Requires env vars:
    CLICKHOUSE_API_ID       ClickHouse Cloud API key ID
    CLICKHOUSE_API_SECRET   ClickHouse Cloud API key secret

Usage:
    ./sync-dashboards.py              # sync all dashboards
    ./sync-dashboards.py hosts        # sync one by name
"""

import base64
import json
import os
import sys
import urllib.request
import urllib.error

ORG_ID = "76e3b458-f59b-4d98-a1c6-f45b8d87f6ec"
SERVICE_ID = "3d718833-6a3d-46f4-90e3-329159975f64"
METRICS_SOURCE_ID = "69cc2aaa2562ef5f27226375"
BASE_URL = f"https://api.clickhouse.cloud/v1/organizations/{ORG_ID}/services/{SERVICE_ID}/clickstack"

GROUP_BY_HOST = "ResourceAttributes['service.instance.id']"


# ---------------------------------------------------------------------------
# Tile helpers
# ---------------------------------------------------------------------------

def metric_tile(name, metric_name, metric_type, agg_fn="avg",
                x=0, y=0, w=24, h=8, where=""):
    """A single metrics line chart tile."""
    return {
        "x": x, "y": y, "w": w, "h": h,
        "name": name,
        "config": {
            "displayType": "line",
            "sourceId": METRICS_SOURCE_ID,
            "select": [{
                "valueExpression": "Value",
                "metricName": metric_name,
                "metricType": metric_type,
                "aggFn": agg_fn,
                "where": where,
                "whereLanguage": "sql",
            }],
            "groupBy": GROUP_BY_HOST,
        },
    }


def row_of_2(y, left_name, left_metric, left_type,
             right_name, right_metric, right_type,
             agg_fn="avg", h=8):
    """Two half-width tiles side by side."""
    return [
        metric_tile(left_name, left_metric, left_type, agg_fn, x=0, y=y, w=12, h=h),
        metric_tile(right_name, right_metric, right_type, agg_fn, x=12, y=y, w=12, h=h),
    ]


# ---------------------------------------------------------------------------
# Dashboard definitions
# ---------------------------------------------------------------------------

DASHBOARDS = []


def dashboard(name, tags=None):
    """Decorator to register a dashboard definition."""
    def decorator(fn):
        DASHBOARDS.append({"name": name, "build": fn, "tags": tags or []})
        return fn
    return decorator


@dashboard("Hosts", tags=["hosts", "infrastructure"])
def hosts_dashboard():
    tiles = []
    y = 0

    # PSI
    tiles.append(metric_tile("CPU PSI (some)",
        "node_pressure_cpu_waiting_seconds_total", "sum", y=y))
    y += 8

    tiles.extend(row_of_2(y,
        "Memory PSI (some)", "node_pressure_memory_waiting_seconds_total", "sum",
        "Memory PSI (full)", "node_pressure_memory_stalled_seconds_total", "sum"))
    y += 8

    tiles.extend(row_of_2(y,
        "IO PSI (some)", "node_pressure_io_waiting_seconds_total", "sum",
        "IO PSI (full)", "node_pressure_io_stalled_seconds_total", "sum"))
    y += 8

    # Load
    tiles.extend(row_of_2(y,
        "Load Average (1m)", "node_load1", "gauge",
        "Load Average (15m)", "node_load15", "gauge",
        agg_fn="last_value"))
    y += 8

    # Memory
    tiles.extend(row_of_2(y,
        "Memory Available", "node_memory_MemAvailable_bytes", "gauge",
        "Memory Total", "node_memory_MemTotal_bytes", "gauge",
        agg_fn="last_value"))
    y += 8

    # Swap
    tiles.extend(row_of_2(y,
        "Swap Total", "node_memory_SwapTotal_bytes", "gauge",
        "Swap Free", "node_memory_SwapFree_bytes", "gauge",
        agg_fn="last_value"))
    y += 8

    # CPU (non-idle seconds — not a percentage, but useful for relative comparison)
    tiles.append(metric_tile("CPU Non-Idle (seconds/interval)",
        "node_cpu_seconds_total", "sum", agg_fn="sum", y=y,
        where="Attributes['mode'] != 'idle'"))

    return tiles


# ---------------------------------------------------------------------------
# API client
# ---------------------------------------------------------------------------

def get_auth_header():
    api_id = os.environ.get("CLICKHOUSE_API_ID", "")
    api_secret = os.environ.get("CLICKHOUSE_API_SECRET", "")
    if not api_id or not api_secret:
        print("ERROR: CLICKHOUSE_API_ID and CLICKHOUSE_API_SECRET must be set", file=sys.stderr)
        sys.exit(1)
    creds = base64.b64encode(f"{api_id}:{api_secret}".encode()).decode()
    return f"Basic {creds}"


def api_request(method, path, body=None):
    url = f"{BASE_URL}{path}"
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", get_auth_header())
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        error_body = e.read().decode()
        try:
            return json.loads(error_body)
        except json.JSONDecodeError:
            return {"error": error_body, "status": e.code}


def get_existing_dashboards():
    resp = api_request("GET", "/dashboards")
    return {d["name"]: d["id"] for d in resp.get("result", [])}


def sync_dashboard(defn, existing):
    name = defn["name"]
    tiles = defn["build"]()
    body = {"name": name, "tiles": tiles, "tags": defn["tags"]}

    dash_id = existing.get(name)
    if dash_id:
        print(f"Updating '{name}' (id: {dash_id})...")
        resp = api_request("PUT", f"/dashboards/{dash_id}", body)
    else:
        print(f"Creating '{name}'...")
        resp = api_request("POST", "/dashboards", body)

    error = resp.get("error", "")
    if error:
        print(f"  ERROR: {error}")
    else:
        new_id = resp.get("result", {}).get("id", "?")
        print(f"  OK (id: {new_id})")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    # Optional: dump JSON for a dashboard without syncing
    if len(sys.argv) > 1 and sys.argv[1] == "--dump":
        name = sys.argv[2] if len(sys.argv) > 2 else None
        for defn in DASHBOARDS:
            if name and defn["name"].lower() != name.lower():
                continue
            body = {"name": defn["name"], "tiles": defn["build"](), "tags": defn["tags"]}
            print(json.dumps(body, indent=2))
        return

    print("Fetching existing dashboards...")
    existing = get_existing_dashboards()

    targets = sys.argv[1:]
    for defn in DASHBOARDS:
        if targets and defn["name"].lower() not in [t.lower() for t in targets]:
            continue
        sync_dashboard(defn, existing)

    print("Done.")


if __name__ == "__main__":
    main()
