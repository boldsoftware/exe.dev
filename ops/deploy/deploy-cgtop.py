#!/usr/bin/env python3
"""Deploy cgtop to exelet hosts.

Builds cgtop locally for linux/amd64, copies the binary and systemd service
file to each host, and restarts the service.

Usage:
    deploy-cgtop.py HOST [HOST ...]

If no hosts are given, the script prints usage and exits. You can pass all
exelet hosts at once:

    deploy-cgtop.py $(ssh exe.dev exelets --json | jq '.exelets[].host' -r)
"""

import subprocess, sys, os, datetime, tempfile

REPO = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()
TIMESTAMP = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
BINARY_NAME = f"cgtop.{TIMESTAMP}"
SERVICE_SRC = os.path.join(REPO, "ops/deploy/cgtop.service")

def run(cmd, **kw):
    print(f"  $ {cmd if isinstance(cmd, str) else ' '.join(cmd)}")
    subprocess.run(cmd, check=True, **kw)

def ssh(host, script):
    run(["ssh", "-o", "StrictHostKeyChecking=no", host, "bash -s"], input=script, text=True)

def scp(src, host, dst):
    run(["scp", src, f"{host}:{dst}"])

if len(sys.argv) < 2:
    print(__doc__, file=sys.stderr)
    sys.exit(1)

hosts = [h if "@" in h else f"ubuntu@{h}" for h in sys.argv[1:]]

# Build locally for linux/amd64
print(f"=== Building cgtop ({BINARY_NAME}) ===\n")
binary_path = os.path.join(tempfile.gettempdir(), BINARY_NAME)
run(
    ["go", "build", "-ldflags=-s -w", "-o", binary_path, "./cmd/cgtop"],
    cwd=REPO,
    env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"},
)

failed_hosts = []

for host in hosts:
    print(f"\n=== Deploying to {host} ===\n")
    try:
        # Check connectivity
        run(["ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", host, "true"])

        # Copy binary and service file
        scp(binary_path, host, f"~/{BINARY_NAME}")
        scp(SERVICE_SRC, host, "~/cgtop.service")

        # Install binary, service, and restart
        ssh(host, f"""
set -e
chmod +x ~/{BINARY_NAME}
sudo mv ~/{BINARY_NAME} /usr/local/bin/cgtop
sudo mv ~/cgtop.service /etc/systemd/system/cgtop.service
sudo systemctl daemon-reload
sudo systemctl enable cgtop
sudo systemctl restart cgtop
sleep 1
sudo systemctl is-active cgtop
curl -sf http://$(tailscale ip -4):9090/debug/gitsha
echo ""
echo "cgtop running on $(hostname)"
""")
    except subprocess.CalledProcessError as e:
        print(f"ERROR: deploy to {host} failed: {e}")
        failed_hosts.append(host)

# Cleanup local binary
os.remove(binary_path)

if failed_hosts:
    print(f"\n=== FAILED on: {', '.join(failed_hosts)} ===")
    sys.exit(1)

print(f"\n=== Done. Deployed cgtop to {len(hosts) - len(failed_hosts)}/{len(hosts)} hosts ===")
