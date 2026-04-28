#!/usr/bin/env python3
"""Discover exelet hosts from tailscale and generate Prometheus file_sd target files.

Queries `tailscale status --json` to find all exelet, replica, and legacy exe-ctr
hosts, then writes Prometheus file_sd JSON files for both the node-exporter (port 9100)
and exelet application metrics (port 9081) jobs.

Output files (in TARGET_DIR, default /etc/prometheus/targets):
  node-exelets-prod.json      - node_exporter targets for production exelets/replicas/exe-ctr
  node-exelets-staging.json   - node_exporter targets for staging exelets/replicas
  exelet-prod.json            - exelet app metrics for production (no replicas)
  exelet-staging.json         - exelet app metrics for staging (no replicas)

Run periodically (e.g. every 5 minutes via systemd timer) on the mon host.
"""

import argparse
import json
import os
import subprocess
import tempfile


DEFAULT_TARGET_DIR = "/etc/prometheus/targets"


def get_tailscale_peers():
    result = subprocess.run(
        ["tailscale", "status", "--json"],
        capture_output=True, text=True, check=True,
    )
    data = json.loads(result.stdout)
    return data.get("Peer", {})


def classify_hosts(peers):
    """Classify peers into exelets, replicas, and legacy exe-ctr hosts by stage."""
    exelets_prod = []
    exelets_staging = []
    replicas_prod = []
    replicas_staging = []
    exe_ctr_prod = []

    for _key, peer in peers.items():
        tags = set(peer.get("Tags", []))
        hostname = peer.get("HostName", "")
        if not hostname:
            continue

        if "tag:exelet" in tags:
            if "tag:prod" in tags:
                exelets_prod.append(hostname)
            elif "tag:staging" in tags:
                exelets_staging.append(hostname)

        elif "tag:replica" in tags and hostname.startswith("exelet-"):
            if "tag:prod" in tags:
                replicas_prod.append(hostname)
            elif "tag:staging" in tags:
                replicas_staging.append(hostname)

        elif hostname.startswith("exe-ctr-") and "tag:prod" in tags:
            exe_ctr_prod.append(hostname)

    return {
        "exelets_prod": sorted(exelets_prod),
        "exelets_staging": sorted(exelets_staging),
        "replicas_prod": sorted(replicas_prod),
        "replicas_staging": sorted(replicas_staging),
        "exe_ctr_prod": sorted(exe_ctr_prod),
    }


def make_target_group(hosts, port, labels):
    if not hosts:
        return None
    return {
        "targets": [f"{h}:{port}" for h in hosts],
        "labels": labels,
    }


def generate_files(classified):
    files = {}

    # node-exelets-prod.json: all prod exelets + replicas + exe-ctr at :9100
    node_prod = sorted(
        classified["exelets_prod"]
        + classified["replicas_prod"]
        + classified["exe_ctr_prod"]
    )
    groups = []
    g = make_target_group(node_prod, 9100, {"stage": "production", "role": "exelet"})
    if g:
        groups.append(g)
    files["node-exelets-prod.json"] = groups

    # node-exelets-staging.json
    node_staging = sorted(
        classified["exelets_staging"] + classified["replicas_staging"]
    )
    groups = []
    g = make_target_group(node_staging, 9100, {"stage": "staging", "role": "exelet"})
    if g:
        groups.append(g)
    files["node-exelets-staging.json"] = groups

    # exelet-prod.json: primary exelets + exe-ctr at :9081 (NOT replicas)
    app_prod = sorted(classified["exelets_prod"] + classified["exe_ctr_prod"])
    groups = []
    g = make_target_group(app_prod, 9081, {"stage": "production"})
    if g:
        groups.append(g)
    files["exelet-prod.json"] = groups

    # exelet-staging.json: staging exelets at :9081 (NOT replicas)
    groups = []
    g = make_target_group(classified["exelets_staging"], 9081, {"stage": "staging"})
    if g:
        groups.append(g)
    files["exelet-staging.json"] = groups

    return files


def atomic_write(path, content):
    """Write file atomically via tmp+rename to avoid partial reads."""
    dir_ = os.path.dirname(path)
    fd, tmp = tempfile.mkstemp(dir=dir_, suffix=".tmp")
    try:
        os.fchmod(fd, 0o644)
        os.write(fd, content.encode())
        os.close(fd)
        os.rename(tmp, path)
    except BaseException:
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def main():
    parser = argparse.ArgumentParser(description="Discover exelet hosts from tailscale for Prometheus file_sd")
    parser.add_argument("target_dir", nargs="?",
                        default=os.environ.get("TARGET_DIR", DEFAULT_TARGET_DIR),
                        help=f"Directory to write target JSON files (default: {DEFAULT_TARGET_DIR}, or $TARGET_DIR)")
    args = parser.parse_args()

    target_dir = args.target_dir
    os.makedirs(target_dir, exist_ok=True)

    peers = get_tailscale_peers()
    classified = classify_hosts(peers)

    total = sum(len(v) for v in classified.values())
    print(f"Discovered {total} hosts:")
    for category, hosts in sorted(classified.items()):
        print(f"  {category}: {len(hosts)}")

    files = generate_files(classified)
    for filename, content in sorted(files.items()):
        path = os.path.join(target_dir, filename)
        atomic_write(path, json.dumps(content, indent=2) + "\n")
        targets = sum(len(g["targets"]) for g in content)
        print(f"Wrote {path} ({targets} targets)")


if __name__ == "__main__":
    main()
