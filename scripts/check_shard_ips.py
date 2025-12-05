#!/usr/bin/env python3
"""Check that base domain and all shard subdomains have distinct IP addresses."""

import socket
import sys
from collections import defaultdict


def get_ip(hostname: str) -> str | None:
    """Get IP address for a hostname, or None if lookup fails."""
    try:
        return socket.gethostbyname(hostname)
    except socket.gaierror:
        return None


def main():
    base_domain = sys.argv[1] if len(sys.argv) > 1 else "exe.xyz"

    # Collect all hostnames and their IPs
    hosts_to_ips = {}

    # Check base domain
    base_ip = get_ip(base_domain)
    if base_ip is None:
        print(f"ERROR: Failed to resolve {base_domain}")
        sys.exit(1)
    hosts_to_ips[base_domain] = base_ip

    # Check all shards (s001 through s025)
    for i in range(1, 26):
        shard_hostname = f"s{i:03d}.{base_domain}"
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

    print(f"OK: All {len(hosts_to_ips)} hostnames for {base_domain} have distinct IP addresses")
    sys.exit(0)


if __name__ == "__main__":
    main()
