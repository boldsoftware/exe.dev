#!/usr/bin/env python3
"""Deploy metricsd to staging or prod.

Builds locally for linux/amd64 (CGO enabled for DuckDB), copies the binary
and systemd service file to the target host, and restarts the service.
"""

import subprocess, sys, os, datetime, time, atexit, tempfile

ENVS = {
    "staging": {
        "host": "ubuntu@exed-staging-01",
        "service_file": "metricsd-staging.service",
        "notify_name": "metricsd-staging",
    },
    "prod": {
        "host": "ubuntu@exed-02",
        "service_file": "metricsd-prod.service",
        "notify_name": "metricsd",
    },
}

if len(sys.argv) < 2 or sys.argv[1] not in ENVS:
    print(f"Usage: {sys.argv[0]} <staging|prod> [-f]", file=sys.stderr)
    sys.exit(1)

env = ENVS[sys.argv[1]]
HOST = env["host"]
TIMESTAMP = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
SHA = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
BINARY_NAME = f"metricsd.{TIMESTAMP}-{SHA[:12]}"
REPO = subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip()
NOTIFY = os.path.join(REPO, "scripts/deploy-notify.sh")

def run(cmd, **kw):
    print(f"  $ {cmd if isinstance(cmd, str) else ' '.join(cmd)}")
    subprocess.run(cmd, check=True, **kw)

def ssh(script):
    run(["ssh", "-o", "StrictHostKeyChecking=no", HOST, "bash -s"], input=script, text=True)

def scp(src, dst):
    run(["scp", src, f"{HOST}:{dst}"])

def notify(action, *args):
    r = subprocess.run([NOTIFY, action, *args], capture_output=True, text=True)
    return r.stdout.strip()

if not os.environ.get("EXE_SLACK_BOT_TOKEN"):
    print("ERROR: EXE_SLACK_BOT_TOKEN is not set. Deployments require Slack notifications.", file=sys.stderr)
    sys.exit(1)

# Prod deploys require safety checks
if sys.argv[1] == "prod":
    safety = os.path.join(REPO, "scripts/check-deploy-safety.sh")
    run([safety] + sys.argv[2:])

# Slack: post start, mark fail on exit unless we mark complete
deploy_ts = notify("start", env["notify_name"])
failed = True
def on_exit():
    if failed and deploy_ts:
        notify("fail", deploy_ts)
atexit.register(on_exit)

print(f"=== Deploying metricsd to {sys.argv[1]} ({BINARY_NAME}) ===\n")

# Check connectivity
run(["ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", HOST, "true"])

# Build locally for linux/amd64 with CGO enabled (required for DuckDB)
print("=== Building metricsd locally ===\n")
binary_path = os.path.join(tempfile.gettempdir(), BINARY_NAME)
run(
    ["go", "build", "-ldflags=-s -w", "-o", binary_path, "./cmd/metricsd"],
    cwd=REPO,
    env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1"},
)

# Copy binary and service file to remote
scp(binary_path, f"~/{BINARY_NAME}")
scp(os.path.join(REPO, f"ops/deploy/{env['service_file']}"), "~/metricsd.service")

# Verify env file exists on remote
ssh("test -f /etc/default/metricsd || { echo 'ERROR: /etc/default/metricsd not found'; exit 1; }")

# Install binary, symlink, service, and restart
ssh(f"""
set -e
chmod +x ~/{BINARY_NAME}
ln -sf ~/{BINARY_NAME} ~/metricsd.latest
sudo mv ~/metricsd.service /etc/systemd/system/metricsd.service
sudo systemctl daemon-reload
sudo systemctl enable metricsd
sudo systemctl restart metricsd
sleep 2
sudo journalctl -u metricsd -n 5 --no-pager -o cat
""")

# Health check
for i in range(15):
    r = subprocess.run(["ssh", HOST, "curl -sf http://$(tailscale ip -4):21090/debug/gitsha"], capture_output=True)
    if r.returncode == 0:
        print(f"healthy (attempt {i+1})")
        break
    time.sleep(2)
else:
    print("health check timed out")
    sys.exit(1)

# Cleanup local binary
os.remove(binary_path)

failed = False
notify("complete", deploy_ts)
print(f"\n=== Done. View logs: ssh {HOST} journalctl -fu metricsd ===")
