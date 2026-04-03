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
"""

import json
import os
import subprocess
import sys

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
YQ = "github.com/mikefarah/yq/v4@v4.52.5"

ROOT_HOSTS = {"ci.crocodile-vector.ts.net"}

WRAPPER_SRC = os.path.join(SCRIPT_DIR, "node-exporter-wrapper.sh")
WRAPPER_DST = "/usr/local/bin/node-exporter-wrapper"
OVERRIDE_DIR = "/etc/systemd/system/prometheus-node-exporter.service.d"


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

    print(f"\n{'='*50}")
    print(f"Deploying to {target} (port {port})")
    print(f"{'='*50}")

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
for i in $(seq 1 10); do
    if curl -sf -o /dev/null --max-time 2 "http://${{TS_IP}}:{port}/metrics"; then
        count=$(curl -s --max-time 5 "http://${{TS_IP}}:{port}/metrics" | grep -v '^#' | wc -l)
        echo "OK - $count metric lines"
        break
    fi
    if [ $i -eq 10 ]; then
        echo "WARNING: not responding on $TS_IP:{port} after 5s"
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
        print(f"scp failed: {scp.stderr}", file=sys.stderr)
        return False

    result = subprocess.run(
        ["ssh", target, "bash -s"],
        input=script,
        text=True,
        capture_output=True,
    )
    print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)
    if result.returncode != 0:
        print(f"FAILED (exit {result.returncode})")
        return False
    return True


def main():
    all_hosts = load_hosts()

    if "--list" in sys.argv:
        for host, port in sorted(all_hosts.items()):
            print(f"  {host}:{port}")
        print(f"\n{len(all_hosts)} hosts")
        return

    if len(sys.argv) > 1:
        targets = {}
        for name in sys.argv[1:]:
            if name == "--list":
                continue
            if name not in all_hosts:
                print(f"Unknown host: {name} (not in prometheus.yml node job)", file=sys.stderr)
                sys.exit(1)
            targets[name] = all_hosts[name]
    else:
        targets = all_hosts

    print(f"Deploying node-exporter to {len(targets)} hosts")
    failed = []
    for host, port in targets.items():
        if not deploy(host, port):
            failed.append(host)

    print(f"\n{'='*50}")
    if failed:
        print(f"FAILED: {', '.join(failed)}")
        sys.exit(1)
    else:
        print(f"All {len(targets)} hosts deployed successfully")


if __name__ == "__main__":
    main()
