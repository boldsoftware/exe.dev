#!/usr/bin/env python3
"""Deploy metricsd to staging or prod. Pushes source via git, builds on the VM."""

import subprocess, sys, os, datetime, time, atexit

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
REMOTE_REPO = "/home/ubuntu/exe-metricsd-git-repo"
TIMESTAMP = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
BINARY = f"metricsd.{TIMESTAMP}"
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

print(f"=== Deploying metricsd to {sys.argv[1]} ({BINARY}) ===\n")

# Check connectivity
run(["ssh", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", HOST, "true"])

# Ensure bare repo exists on remote, then push
ssh(f"git init --bare -q {REMOTE_REPO}.git 2>/dev/null || true")
run(["git", "push", "--force", "-q", f"{HOST}:{REMOTE_REPO}.git", "HEAD:refs/heads/deploy"], cwd=REPO)

# Build on VM
ssh(f"""
set -e
export PATH="/usr/local/go/bin:$PATH"
if ! command -v go &>/dev/null; then
    echo "Installing Go..."
    sudo apt-get update -qq && sudo apt-get install -y -qq golang-go
fi

if [ -d {REMOTE_REPO} ]; then
    cd {REMOTE_REPO}
    git fetch -q {REMOTE_REPO}.git deploy
    git checkout -q --force FETCH_HEAD
else
    git clone -q {REMOTE_REPO}.git {REMOTE_REPO} -b deploy
    cd {REMOTE_REPO}
fi
git clean -xdf -q
go build -ldflags="-s -w" -o ~/{BINARY} ./cmd/metricsd
ls -lh ~/{BINARY}
""")

# Verify env file exists on remote
ssh("test -f /etc/default/metricsd || { echo 'ERROR: /etc/default/metricsd not found'; exit 1; }")

# Install service
scp(os.path.join(REPO, f"ops/deploy/{env['service_file']}"), "~/metricsd.service")
ssh("""
sudo mv ~/metricsd.service /etc/systemd/system/metricsd.service
sudo systemctl daemon-reload
sudo systemctl enable metricsd
sudo systemctl restart metricsd
sleep 2
sudo journalctl -u metricsd -n 5 --no-pager -o cat
""")

# Health check
for i in range(15):
    r = subprocess.run(["ssh", HOST, "curl -sf http://$(tailscale ip -4):21090/health"], capture_output=True)
    if r.returncode == 0:
        print(f"healthy (attempt {i+1})")
        break
    time.sleep(2)
else:
    print("health check timed out")
    sys.exit(1)

failed = False
notify("complete", deploy_ts)
print(f"\n=== Done. View logs: ssh {HOST} journalctl -fu metricsd ===")
