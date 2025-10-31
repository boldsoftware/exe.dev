#!/usr/bin/env bash
# Run sketch with SSH access to colima-exe-ctr using a dedicated key

# This script:
# 1) Ensures a dedicated key exists at ~/.colima/exe-ctr-colima-ed25519
# 2) Injects its public key into the VM's ubuntu authorized_keys
# 3) Starts sketch mounting only this dedicated key + a minimal SSH config

set -o pipefail

COLIMA_PROFILE=${COLIMA_PROFILE:-exe-ctr}
SSH_PORT=${SSH_PORT:-}
KEY_DIR="$HOME/.colima"
KEY_PATH="$KEY_DIR/exe-ctr-colima-ed25519"

log() { echo "[e2e/run.sh] $*"; }

ensure_key() {
    if [ ! -f "$KEY_PATH" ]; then
        log "Generating dedicated SSH key at $KEY_PATH"
        mkdir -p "$KEY_DIR"
        ssh-keygen -t ed25519 -N "" -f "$KEY_PATH" -C "exe-ctr-colima-e2e" >/dev/null
        chmod 600 "$KEY_PATH"
    else
        log "Dedicated SSH key already exists: $KEY_PATH"
    fi
}

detect_ssh_port() {
    if [ -n "$SSH_PORT" ]; then
        return 0
    fi
    # Prefer colima ssh-config if available
    if command -v colima >/dev/null 2>&1 && colima list 2>/dev/null | grep -q "^${COLIMA_PROFILE}"; then
        local port
        port=$(colima ssh-config -p "$COLIMA_PROFILE" 2>/dev/null | awk '/^\s*Port\s+/ {print $2; exit}')
        if [ -n "$port" ]; then
            SSH_PORT="$port"
            log "Detected SSH port via colima: $SSH_PORT"
            return 0
        fi
    fi
    # Fallback: parse user's ~/.ssh/config
    if [ -f "$HOME/.ssh/config" ]; then
        local in_block="no"
        local port
        while IFS= read -r line; do
            case "$line" in
            "Host colima-exe-ctr") in_block="yes" ;;
            Host\ *) in_block="no" ;;
            *Port\ *) if [ "$in_block" = "yes" ]; then port=$(echo "$line" | awk '{print $2}'); fi ;;
            esac
            if [ -n "$port" ]; then break; fi
        done <"$HOME/.ssh/config"
        if [ -n "$port" ]; then
            SSH_PORT="$port"
            log "Detected SSH port via ~/.ssh/config: $SSH_PORT"
            return 0
        fi
    fi
    # Default fallback
    SSH_PORT=22251
    log "Falling back to default SSH port: $SSH_PORT"
}

inject_key_via_ssh() {
    # Try using existing host SSH config (expected Host colima-exe-ctr)
    if timeout 5 ssh -o ConnectTimeout=3 -o BatchMode=yes colima-exe-ctr true 2>/dev/null; then
        log "Injecting pubkey via ssh colima-exe-ctr"
        ssh colima-exe-ctr 'umask 077; mkdir -p ~/.ssh; touch ~/.ssh/authorized_keys; tmp=$(mktemp); cat > "$tmp"; if ! grep -qxF -f "$tmp" ~/.ssh/authorized_keys; then cat "$tmp" >> ~/.ssh/authorized_keys; fi; rm -f "$tmp"; chmod 700 ~/.ssh; chmod 600 ~/.ssh/authorized_keys' <"${KEY_PATH}.pub"
        return $?
    fi
    return 1
}

inject_key_via_colima() {
    # Fallback to colima if direct SSH is not configured
    if command -v colima >/dev/null 2>&1 && colima list 2>/dev/null | grep -q "^${COLIMA_PROFILE}"; then
        log "Injecting pubkey via colima to profile ${COLIMA_PROFILE}"
        # Copy pubkey to VM then append if missing; ensure correct perms/owner
        cat "${KEY_PATH}.pub" | colima ssh -p "${COLIMA_PROFILE}" -- sh -c 'cat > /tmp/exe-ctr-colima.pub'
        colima ssh -p "${COLIMA_PROFILE}" -- sudo bash -lc 'umask 077; mkdir -p /home/ubuntu/.ssh; touch /home/ubuntu/.ssh/authorized_keys; chmod 700 /home/ubuntu/.ssh; chmod 600 /home/ubuntu/.ssh/authorized_keys; if ! grep -qxF -f /tmp/exe-ctr-colima.pub /home/ubuntu/.ssh/authorized_keys; then cat /tmp/exe-ctr-colima.pub >> /home/ubuntu/.ssh/authorized_keys; fi; chown -R ubuntu:ubuntu /home/ubuntu/.ssh; rm -f /tmp/exe-ctr-colima.pub'
        return $?
    fi
    return 1
}

inject_key() {
    if inject_key_via_ssh; then
        return 0
    fi
    if inject_key_via_colima; then
        return 0
    fi
    log "ERROR: Could not inject SSH key into ${COLIMA_PROFILE}. Is the VM running and reachable?"
    log "       Try: ./ops/setup-colima-host.sh or ensure 'ssh colima-exe-ctr' works."
    return 1
}

# 1) Ensure dedicated key exists
ensure_key || {
    log "Failed to ensure key"
    exit 1
}

# 2) Inject pubkey into VM before starting sketch
inject_key || exit 1

# 3) Create temporary SSH setup for the container (ensure Docker can mount it)
detect_ssh_port
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
mkdir -p "$PROJECT_ROOT/.gotmp"
TEMP_SSH_DIR=$(mktemp -d "$PROJECT_ROOT/.gotmp/ssh.XXXXXX")
trap 'rm -rf "$TEMP_SSH_DIR"' EXIT

cp "$KEY_PATH" "$TEMP_SSH_DIR/id_ed25519"
chmod 600 "$TEMP_SSH_DIR/id_ed25519"

cat >"$TEMP_SSH_DIR/config" <<EOF
Host colima-exe-ctr
    HostName host.docker.internal
    Port ${SSH_PORT}
    User ubuntu
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    IdentitiesOnly yes
    IdentityFile /root/.ssh/id_ed25519
EOF

# Harden permissions so ssh reads the config
chmod 700 "$TEMP_SSH_DIR"
chmod 600 "$TEMP_SSH_DIR/config"

# Run sketch with the SSH configuration mounted (read-only) and container env prepared
log "Mounting SSH dir: $TEMP_SSH_DIR"
ls -la "$TEMP_SSH_DIR" || true

# Build a prompt to run the e2e tests end-to-end inside sketch
read -r -d '' PROMPT <<'EOS'
Goal: Run exed E2E tests end-to-end inside this container.

Do this exactly:
- Verify SSH to the Colima VM works: ssh colima-exe-ctr "echo ok"
- go test ./e2e/expect
- read main-test-prompt.txt and follow the instructions in there.
EOS

sketch -verbose -one-shot \
    -mount "$TEMP_SSH_DIR:/root/.ssh:ro" \
    -docker-args "--add-host=host.docker.internal:host-gateway -e CTR_HOST=ssh://colima-exe-ctr" \
    -prompt "$PROMPT"
