#!/usr/bin/env python3
"""Check that base domain and all shard subdomains have distinct IP addresses.

Usage: check_shard_ips.py [domain] [num_shards]
  domain: Base domain to check (default: exe.xyz)
  num_shards: Number of shards to check (default: 1016)
"""

import socket
import ssl
import sys
import urllib.error
import urllib.request
from collections import defaultdict


def get_ip(hostname: str) -> str | None:
    """Get IP address for a hostname, or None if lookup fails."""
    try:
        return socket.gethostbyname(hostname)
    except socket.gaierror:
        return None


def check_https(hostname: str, timeout: float = 10) -> str | None:
    """Check if HTTPS is responding on the hostname. Returns None on success, error message on failure."""
    url = f"https://{hostname}/"
    try:
        ctx = ssl.create_default_context()
        req = urllib.request.Request(url, method="HEAD")
        with urllib.request.urlopen(req, timeout=timeout, context=ctx):
            pass
        return None
    except urllib.error.HTTPError:
        # HTTP errors (401, 403, etc.) mean the server is responding
        return None
    except Exception as e:
        return str(e)


def main():
    base_domain = sys.argv[1] if len(sys.argv) > 1 else "exe.xyz"
    num_shards = int(sys.argv[2]) if len(sys.argv) > 2 else 1016

    # Collect all hostnames and their IPs
    hosts_to_ips = {}

    # Check base domain
    base_ip = get_ip(base_domain)
    if base_ip is None:
        print(f"ERROR: Failed to resolve {base_domain}")
        sys.exit(1)
    hosts_to_ips[base_domain] = base_ip

    # Check all shards (na001 through naNNN)
    for i in range(1, num_shards + 1):
        shard_hostname = f"na{i:03d}.{base_domain}"
        shard_ip = get_ip(shard_hostname)
        if shard_ip is None:
            print(f"ERROR: Failed to resolve {shard_hostname}")
            sys.exit(1)
        hosts_to_ips[shard_hostname] = shard_ip

    # Check for duplicates
    ip_to_hosts = defaultdict(list)
    for host, ip in hosts_to_ips.items():
        ip_to_hosts[ip].append(host)

    # Find IPs with multiple hosts
    duplicates = {ip: hosts for ip, hosts in ip_to_hosts.items() if len(hosts) > 1}

    if duplicates:
        print(f"ERROR: Found duplicate IP addresses for {base_domain}:")
        for ip, hosts in sorted(duplicates.items()):
            print(f"  {ip}: {', '.join(sorted(hosts))}")
        sys.exit(1)

    # Print all hostname to IP mappings
    for host, ip in sorted(hosts_to_ips.items()):
        print(f"{host} -> {ip}")

    print(
        f"\nOK: All {len(hosts_to_ips)} hostnames for {base_domain} have distinct IP addresses"
    )

    # Check HTTPS connectivity for all shard hostnames
    print(
        f"\nChecking HTTPS connectivity for {num_shards} shards: ", end="", flush=True
    )
    https_errors = []
    for i in range(1, num_shards + 1):
        shard_hostname = f"na{i:03d}.{base_domain}"
        err = check_https(shard_hostname)
        if err:
            https_errors.append((shard_hostname, err))
        else:
            print(".", end="", flush=True)
    print()

    if https_errors:
        print(f"ERROR: {len(https_errors)} shard(s) failed HTTPS check:")
        for host, err in https_errors:
            print(f"  {host}: {err}")
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
