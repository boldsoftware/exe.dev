#!/bin/bash
# Deploy the whoami sqlite database onto the production host

set -euo pipefail

INSTANCE_NAME="${INSTANCE_NAME:-exed-01}"
REMOTE_USER="${REMOTE_USER:-ubuntu}"
TAILSCALE_HOST="${REMOTE_USER}@${INSTANCE_NAME}"
REMOTE_DIR="${REMOTE_DIR:-/home/${REMOTE_USER}/ghuser}"
REMOTE_BASENAME="whoami.sqlite3"
REMOTE_PREFIX="${REMOTE_DIR}/${REMOTE_BASENAME}"

B2_KEY_ID="004edb881590a7d0000000008"
B2_KEY="K004hvv/i5raZbvKXARk+H7sZLZ5XtQ"
B2_BUCKET="bold-exe"
B2_OBJECT="whoami3.sqlite3.zst"

printf 'Deploying %s to %s...\n' "${B2_OBJECT}" "${TAILSCALE_HOST}"

if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "${TAILSCALE_HOST}" "echo >/dev/null"; then
    printf 'ERROR: unable to reach %s via SSH\n' "${TAILSCALE_HOST}" >&2
    exit 1
fi

printf -v REMOTE_DIR_Q '%q' "${REMOTE_DIR}"
printf -v REMOTE_PREFIX_Q '%q' "${REMOTE_PREFIX}"
printf -v REMOTE_BASENAME_Q '%q' "${REMOTE_BASENAME}"
printf -v B2_KEY_ID_Q '%q' "${B2_KEY_ID}"
printf -v B2_KEY_Q '%q' "${B2_KEY}"
printf -v B2_BUCKET_Q '%q' "${B2_BUCKET}"
printf -v B2_OBJECT_Q '%q' "${B2_OBJECT}"

ssh -o StrictHostKeyChecking=no "${TAILSCALE_HOST}" 'bash -se' <<REMOTE
set -euo pipefail

REMOTE_DIR=${REMOTE_DIR_Q}
REMOTE_PREFIX=${REMOTE_PREFIX_Q}
REMOTE_BASENAME=${REMOTE_BASENAME_Q}
B2_KEY_ID=${B2_KEY_ID_Q}
B2_KEY=${B2_KEY_Q}
B2_BUCKET=${B2_BUCKET_Q}
B2_OBJECT=${B2_OBJECT_Q}

export PATH="\$PATH:/home/ubuntu/.local/bin"

if ! command -v b2 >/dev/null 2>&1; then
    echo "b2 command not found on remote host" >&2
    # if you hit this error: install it with pipx
    exit 1
fi

if ! command -v zstd >/dev/null 2>&1; then
    echo "zstd command not found on remote host" >&2
    exit 1
fi

mkdir -p "\${REMOTE_DIR}"

export B2_APPLICATION_KEY_ID="\${B2_KEY_ID}"
export B2_APPLICATION_KEY="\${B2_KEY}"

if ! b2 account authorize >/dev/null 2>&1; then
    echo "failed to authorize with Backblaze B2" >&2
    exit 1
fi

TIMESTAMP="\$(date +%Y%m%d-%H%M%S)"
REMOTE_ZST="\${REMOTE_PREFIX}.\${TIMESTAMP}.zst"
REMOTE_DB="\${REMOTE_PREFIX}.\${TIMESTAMP}"

cleanup() {
    rm -f "\${REMOTE_ZST}"
}
trap cleanup EXIT

echo "Downloading \${B2_OBJECT} to \${REMOTE_ZST}."
if ! b2 file download "b2://\${B2_BUCKET}/\${B2_OBJECT}" "\${REMOTE_ZST}"; then
    echo "failed to download \${B2_OBJECT}" >&2
    exit 1
fi

echo "Decompressing to \${REMOTE_DB}."
if ! zstd -d "\${REMOTE_ZST}" -o "\${REMOTE_DB}"; then
    echo "failed to decompress \${REMOTE_ZST}" >&2
    exit 1
fi

ln -sfn "\${REMOTE_DB}" "\${REMOTE_PREFIX}.latest"
ln -sfn "\${REMOTE_PREFIX}.latest" "\${REMOTE_PREFIX}"

chmod 0640 "\${REMOTE_DB}" || true

ls -lh "\${REMOTE_DB}"
echo "Updated symlink: \$(readlink -f "\${REMOTE_PREFIX}.latest")"
REMOTE
