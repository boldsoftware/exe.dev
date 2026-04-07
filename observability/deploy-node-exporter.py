#!/usr/bin/env python3
"""Deploy node_exporter to all hosts.

Reads the host list from prometheus.yml (job_name: 'node'), so there's a
single source of truth for which hosts run node_exporter and on which port.

Installs prometheus-node-exporter via apt if needed, deploys the wrapper
script (node-exporter-wrapper.sh) that binds to the Tailscale IP, and
restarts if config changed.

Usage:
    ./deploy-node-exporter.py                            # all hosts
    ./deploy-node-exporter.py exelet-lax2-prod-01 mon    # specific hosts
    ./deploy-node-exporter.py --list                     # show hosts and exit
    ./deploy-node-exporter.py -j 10                      # deploy 10 hosts in parallel (default: 5)
"""

import json
import os
import subprocess
import sys
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
YQ = "github.com/mikefarah/yq/v4@v4.52.5"

ROOT_HOSTS = {"ci.crocodile-vector.ts.net"}

WRAPPER_SRC = os.path.join(SCRIPT_DIR, "node-exporter-wrapper.sh")
WRAPPER_DST = "/usr/local/bin/node-exporter-wrapper"
OVERRIDE_DIR = "/etc/systemd/system/prometheus-node-exporter.service.d"

_print_lock = threading.Lock()


def make_override(port):
    return (
        "[Unit]\n"
        "After=tailscaled.service\n"
        "Wants=tailscaled.service\n"
        "\n"
        "[Service]\n"
        f"Environment=PORT={port}\n"
        "ExecStart=\n"
        f"ExecStart={WRAPPER_DST}\n"
    )


def load_hosts():
    """Parse prometheus.yml and return {hostname: port} for all node job targets."""
    prom_yml = os.path.join(SCRIPT_DIR, "prometheus.yml")
    result = subprocess.run(
        ["go", "run", YQ, "-o", "json", prom_yml],
        capture_output=True, text=True, check=True,
    )
    config = json.loads(result.stdout)

    hosts = {}
    for job in config["scrape_configs"]:
        if job["job_name"] != "node":
            continue
        for group in job["static_configs"]:
            for target in group["targets"]:
                host, port_str = target.rsplit(":", 1)
                hosts[host] = int(port_str)
    return hosts


def deploy(host, port):
    user = "root" if host in ROOT_HOSTS else "ubuntu"
    target = f"{user}@{host}"
    override = make_override(port)
    out_lines = []

    # Read current wrapper content on host (if any)
    with open(WRAPPER_SRC) as f:
        new_wrapper = f.read()

    script = f"""\
set -euo pipefail

if ! command -v prometheus-node-exporter &>/dev/null; then
    echo "Installing prometheus-node-exporter..."
    sudo apt-get update && sudo apt-get install -y prometheus-node-exporter
fi

CHANGED=0

OLD_WRAPPER=$(cat {WRAPPER_DST} 2>/dev/null || true)
NEW_WRAPPER=$(cat /tmp/node-exporter-wrapper.sh)
if [ "$OLD_WRAPPER" != "$NEW_WRAPPER" ]; then
    sudo install -m 0755 /tmp/node-exporter-wrapper.sh {WRAPPER_DST}
    CHANGED=1
fi
rm -f /tmp/node-exporter-wrapper.sh

sudo mkdir -p {OVERRIDE_DIR}
OVERRIDE_FILE={OVERRIDE_DIR}/override.conf
OLD_OVERRIDE=$(cat "$OVERRIDE_FILE" 2>/dev/null || true)
NEW_OVERRIDE='{override}'
if [ "$OLD_OVERRIDE" != "$NEW_OVERRIDE" ]; then
    printf '%s\\n' "$NEW_OVERRIDE" | sudo tee "$OVERRIDE_FILE" > /dev/null
    CHANGED=1
fi

if [ "$CHANGED" -eq 1 ]; then
    sudo systemctl daemon-reload
    if ! systemctl is-enabled prometheus-node-exporter &>/dev/null; then
        sudo systemctl enable prometheus-node-exporter
    fi
    sudo systemctl restart prometheus-node-exporter
    echo "Restarted with new config"
else
    echo "Config unchanged"
fi

TS_IP=$(tailscale ip -4)
for i in $(seq 1 5); do
    BODY=$(curl -s --max-time 3 "http://${{TS_IP}}:{port}/metrics" 2>/dev/null) && {{
        count=$(echo "$BODY" | grep -vc '^#')
        echo "OK - $count metric lines"
        break
    }}
    if [ $i -eq 5 ]; then
        echo "WARNING: not responding on $TS_IP:{port} after retries"
    fi
    sleep 0.5
done
"""

    # scp wrapper, then run setup
    scp = subprocess.run(
        ["scp", WRAPPER_SRC, f"{target}:/tmp/node-exporter-wrapper.sh"],
        capture_output=True, text=True,
    )
    if scp.returncode != 0:
        out_lines.append(f"scp failed: {scp.stderr}")
        with _print_lock:
            print("\n".join(out_lines), flush=True)
        return False

    result = subprocess.run(
        ["ssh", target, "bash -s"],
        input=script,
        text=True,
        capture_output=True,
    )
    if result.stdout:
        out_lines.append(result.stdout.rstrip())
    if result.stderr:
        out_lines.append(result.stderr.rstrip())
    if result.returncode != 0:
        out_lines.append(f"FAILED (exit {result.returncode})")
        with _print_lock:
            print("\n".join(out_lines), flush=True)
        return False
    with _print_lock:
        print("\n".join(out_lines), flush=True)
    return True


def parse_args(argv):
    """Parse arguments, extracting -j N for parallelism."""
    parallel = 5
    rest = []
    it = iter(argv[1:])
    for arg in it:
        if arg == "-j":
            try:
                parallel = int(next(it))
            except (StopIteration, ValueError):
                print("ERROR: -j requires an integer argument", file=sys.stderr)
                sys.exit(1)
        elif arg.startswith("-j") and len(arg) > 2:
            try:
                parallel = int(arg[2:])
            except ValueError:
                print(f"ERROR: invalid -j value: {arg[2:]}", file=sys.stderr)
                sys.exit(1)
        else:
            rest.append(arg)
    return parallel, rest


def main():
    all_hosts = load_hosts()
    parallel, rest = parse_args(sys.argv)

    if "--list" in rest:
        for host, port in sorted(all_hosts.items()):
            print(f"  {host}:{port}")
        print(f"\n{len(all_hosts)} hosts")
        return

    if rest:
        targets = {}
        for name in rest:
            if name not in all_hosts:
                print(f"Unknown host: {name} (not in prometheus.yml node job)", file=sys.stderr)
                sys.exit(1)
            targets[name] = all_hosts[name]
    else:
        targets = all_hosts

    total = len(targets)
    hosts_label = "host" if total == 1 else "hosts"
    print(f"Deploying node-exporter to {total} {hosts_label} ({parallel} at a time)")
    failed = []
    done = 0
    in_flight = set()
    with ThreadPoolExecutor(max_workers=parallel) as pool:
        futures = {}
        for host, port in targets.items():
            f = pool.submit(deploy, host, port)
            futures[f] = host
            in_flight.add(host)
        with _print_lock:
            print(f"[0/{total}] started: {', '.join(sorted(in_flight))}", flush=True)
        for future in as_completed(futures):
            host = futures[future]
            in_flight.discard(host)
            done += 1
            ok = True
            try:
                if not future.result():
                    ok = False
                    failed.append(host)
            except Exception as e:
                print(f"{host}: exception: {e}", file=sys.stderr)
                ok = False
                failed.append(host)
            with _print_lock:
                status = "ok" if ok else "FAILED"
                waiting = ", ".join(sorted(in_flight))
                print(f"[{done}/{total}] {host}: {status} | waiting on: {waiting or 'none'}", flush=True)

    print(f"\n{'='*50}")
    if failed:
        print(f"FAILED: {', '.join(failed)}")
        sys.exit(1)
    else:
        print(f"All {total} hosts deployed successfully")


if __name__ == "__main__":
    main()
