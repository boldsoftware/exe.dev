#!/usr/bin/env bash
#
# Deploy CI runner infrastructure to edric.
#
# Usage: ./ops/ci/deploy-edric.sh
#
# Deploys systemd services, cron scripts, and ensures consistent
# configuration across all 16 runners (8 e1e + 8 ci).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOST="root@edric"
NUM_RUNNERS=8
GO_VERSION="1.25.7"

echo "=== Deploying CI infrastructure to edric ==="

# --- Go installation ---
echo "--- Checking Go version ---"
CURRENT_GO=$(ssh "$HOST" '/usr/local/go/bin/go version 2>/dev/null || echo "none"')
if echo "$CURRENT_GO" | grep -q "go${GO_VERSION}"; then
    echo "Go ${GO_VERSION} already installed"
else
    echo "Installing Go ${GO_VERSION} (current: ${CURRENT_GO})"
    ssh "$HOST" "set -euo pipefail; curl -fsSL 'https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz' -o /tmp/go.tar.gz && rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz"
    ssh "$HOST" "/usr/local/go/bin/go version"
fi

# --- Generate and deploy systemd service files ---
echo "--- Deploying systemd services ---"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

for i in $(seq 0 $((NUM_RUNNERS - 1))); do
    # e1e runner service
    sed "s/%i/${i}/g" "$SCRIPT_DIR/edric-e1e.service" >"$TMPDIR/actions.runner.boldsoftware.edric-${i}.service"
    # CI runner service
    sed "s/%i/${i}/g" "$SCRIPT_DIR/edric-ci.service" >"$TMPDIR/actions.runner.boldsoftware.edric-ci-${i}.service"
done

scp "$TMPDIR"/actions.runner.boldsoftware.edric*.service "$HOST:/etc/systemd/system/"

# --- Deploy scripts ---
echo "--- Deploying scripts ---"
scp "$SCRIPT_DIR/edric-ci-warmup.sh" "$SCRIPT_DIR/edric-ci-cleanup.sh" "$HOST:/usr/local/bin/"
ssh "$HOST" "chmod +x /usr/local/bin/edric-ci-warmup.sh /usr/local/bin/edric-ci-cleanup.sh"

# --- Deploy cron ---
echo "--- Deploying cron config ---"
scp "$SCRIPT_DIR/edric-ci.cron" "$HOST:/etc/cron.d/edric-ci"
ssh "$HOST" "chmod 644 /etc/cron.d/edric-ci"

# --- Ensure user groups ---
echo "--- Ensuring user groups ---"
ssh "$HOST" 'set -euo pipefail
    getent group ci-runners >/dev/null || groupadd ci-runners
    getent group docker >/dev/null || true
    for i in $(seq 0 7); do
        USER="runner${i}"
        for GROUP in ci-runners docker kvm libvirt; do
            if getent group "$GROUP" >/dev/null 2>&1; then
                usermod -aG "$GROUP" "$USER" 2>/dev/null || true
            fi
        done
    done
'

# --- Ensure sudoers ---
echo "--- Ensuring sudoers ---"
ssh "$HOST" 'set -euo pipefail
    for i in $(seq 0 7); do
        USER="runner${i}"
        FILE="/etc/sudoers.d/${USER}"
        EXPECTED="${USER} ALL=(ALL) NOPASSWD: ALL"
        if [[ ! -f "$FILE" ]] || [[ "$(cat "$FILE")" != "$EXPECTED" ]]; then
            echo "$EXPECTED" > "$FILE"
            chmod 440 "$FILE"
            echo "  Updated $FILE"
        fi
    done
'

# --- Verify deploy key ---
echo "--- Checking deploy key ---"
ssh "$HOST" '
    KEY="/etc/edric-ci-deploy-key"
    if [[ ! -f "$KEY" ]]; then
        echo "WARNING: Deploy key not found at $KEY"
        echo "  Git prefetch will not work until a deploy key is installed."
    else
        PERMS=$(stat -c "%a %U %G" "$KEY")
        if [[ "$PERMS" != "640 root ci-runners" ]]; then
            echo "Fixing deploy key permissions (was: $PERMS)"
            chown root:ci-runners "$KEY"
            chmod 640 "$KEY"
        else
            echo "Deploy key permissions OK"
        fi
    fi
'

# --- Reload and restart ---
echo "--- Reloading systemd ---"
ssh "$HOST" "systemctl daemon-reload"

# Restart all runner services to pick up changes.
# The runners reconnect to GitHub automatically after restart.
echo "--- Restarting runner services ---"
ssh "$HOST" 'set -euo pipefail
    for i in $(seq 0 7); do
        systemctl restart "actions.runner.boldsoftware.edric-${i}" || echo "WARNING: failed to restart edric-${i}"
        systemctl restart "actions.runner.boldsoftware.edric-ci-${i}" || echo "WARNING: failed to restart edric-ci-${i}"
    done
'

# --- Verify ---
echo "--- Verifying ---"
ssh "$HOST" 'set -euo pipefail
    echo "Go: $(/usr/local/go/bin/go version)"
    FAILED=0
    for i in $(seq 0 7); do
        for SVC in "actions.runner.boldsoftware.edric-${i}" "actions.runner.boldsoftware.edric-ci-${i}"; do
            if ! systemctl is-active --quiet "$SVC"; then
                echo "FAIL: $SVC is not active"
                FAILED=1
            fi
        done
    done
    if [[ $FAILED -eq 0 ]]; then
        echo "All 16 runner services are active"
    else
        echo "Some services failed to start"
        exit 1
    fi
'

echo "=== Deploy complete ==="
