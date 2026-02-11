#!/usr/bin/env bash
#
# Pre-warm CI caches for all runner users on edric.
# Run via cron to ensure CI runs start fast.
#
set -euo pipefail

LOG="/var/log/edric-ci-warmup.log"
exec >>"$LOG" 2>&1
echo "=== $(date) === warmup starting ==="

DEPLOY_KEY="/etc/edric-ci-deploy-key"
GIT_SSH_CMD="ssh -i $DEPLOY_KEY -o StrictHostKeyChecking=accept-new"
PREFETCH_URL="git@github.com:boldsoftware/exe.git"
PREFETCH_REFSPEC="+refs/heads/*:refs/prefetch/remotes/origin/*"

# 0. Pre-cache GitHub Actions tarballs.
# The runners read from ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE before downloading.
# The runner expects: {cache_dir}/{owner}_{repo}/{sha}.tar.gz
ACTIONS_CACHE="/data/actions-archive-cache"
if [[ -d "$ACTIONS_CACHE" ]]; then
    # Find a checked-out repo to read workflow files from.
    REPO_DIR=""
    for d in /home/runner0/_work/exe/exe /home/runner0/_work-ci/exe/exe; do
        if [[ -d "$d/.github/workflows" ]]; then
            REPO_DIR="$d"
            break
        fi
    done

    if [[ -n "$REPO_DIR" ]]; then
        # Extract unique action references (owner/repo@ref) from workflow files.
        ACTIONS=$(grep -rh 'uses:' "$REPO_DIR/.github/workflows/" |
            sed -n 's/.*uses: *\([^/]*\/[^@]*@[^ ]*\).*/\1/p' |
            grep -v '\./' |
            sort -u)

        for ACTION in $ACTIONS; do
            OWNER_REPO="${ACTION%%@*}"
            REF="${ACTION##*@}"
            # The runner replaces / with _ in the directory name.
            DIR_NAME="${OWNER_REPO//\//_}"
            # Resolve the ref to a SHA.
            SHA=$(git ls-remote "https://github.com/${OWNER_REPO}.git" "$REF" 2>/dev/null | head -1 | cut -f1)
            if [[ -z "$SHA" ]]; then
                continue
            fi
            mkdir -p "$ACTIONS_CACHE/$DIR_NAME"
            TARBALL="$ACTIONS_CACHE/$DIR_NAME/${SHA}.tar.gz"
            if [[ -f "$TARBALL" ]]; then
                continue
            fi
            echo "Caching action ${OWNER_REPO}@${REF} (${SHA})"
            curl -fsSL -o "$TARBALL.tmp" \
                "https://api.github.com/repos/${OWNER_REPO}/tarball/${SHA}" &&
                mv "$TARBALL.tmp" "$TARBALL" ||
                rm -f "$TARBALL.tmp"
        done

        # Prune tarballs older than 30 days.
        find "$ACTIONS_CACHE" -name '*.tar.gz' -mtime +30 -delete 2>/dev/null || true
        # Clean up empty subdirectories.
        find "$ACTIONS_CACHE" -mindepth 1 -type d -empty -delete 2>/dev/null || true
    fi
fi

for i in $(seq 0 7); do
    USER="runner${i}"
    USER_HOME="/home/${USER}"

    echo "--- warming ${USER} ---"

    # 1. Git prefetch in both workdirs (e1e and ci).
    # Fetches to refs/prefetch/ so it never conflicts with actions/checkout.
    # Objects are shared, so subsequent checkouts are fast.
    for WORKDIR in "${USER_HOME}/_work/exe/exe" "${USER_HOME}/_work-ci/exe/exe"; do
        if [[ -d "${WORKDIR}/.git" ]]; then
            su - "$USER" -c "GIT_SSH_COMMAND='$GIT_SSH_CMD' git -C ${WORKDIR} fetch --quiet $PREFETCH_URL $PREFETCH_REFSPEC" || true
        fi
    done

    # 2. Go module download + build cache (use e1e workdir if it exists, else ci)
    WORKDIR=""
    if [[ -f "${USER_HOME}/_work/exe/exe/go.mod" ]]; then
        WORKDIR="${USER_HOME}/_work/exe/exe"
    elif [[ -f "${USER_HOME}/_work-ci/exe/exe/go.mod" ]]; then
        WORKDIR="${USER_HOME}/_work-ci/exe/exe"
    fi

    if [[ -n "$WORKDIR" ]]; then
        # Restore any tracked files that were accidentally deleted (e.g. by a
        # previous make exelet-fs with empty GOARCH). Without this, go build
        # fails because the exelet/fs Go source files are missing.
        su - "$USER" -c "cd ${WORKDIR} && git checkout -- exelet/fs/*.go exelet/fs/*/.gitkeep" 2>/dev/null || true

        su - "$USER" -c "cd ${WORKDIR} && go mod download" || true
        # Download exelet-fs BEFORE go build, since go build needs the
        # embedded files that make exelet-fs provides.
        su - "$USER" -c "cd ${WORKDIR} && make exelet-fs" || true
        su - "$USER" -c "cd ${WORKDIR} && go build ./..." || true

        # 2b. Shelley Go module download + build cache
        if [[ -f "${WORKDIR}/shelley/go.mod" ]]; then
            su - "$USER" -c "cd ${WORKDIR}/shelley && go mod download" || true
            su - "$USER" -c "cd ${WORKDIR}/shelley && go build ./..." || true
        fi

        # 2c. Shelley UI dependencies
        if [[ -f "${WORKDIR}/shelley/ui/pnpm-lock.yaml" ]]; then
            su - "$USER" -c "cd ${WORKDIR}/shelley/ui && pnpm install --frozen-lockfile --silent" || true
        fi
    fi

    # 3. VM snapshot (only needed for e1e runners, and only runner0 needs to
    #    actually create it since the backing images are shared).
    #    Use flock to prevent concurrent cron invocations from starting multiple
    #    VMs when the snapshot doesn't exist yet (e.g., after midnight date rollover).
    if [[ $i -eq 0 && -d "${USER_HOME}/_work/exe/exe/ops" ]]; then
        # Clean up any VMs left from previous failed snapshot attempts.
        for VM in $(virsh list --name 2>/dev/null | grep "^ci-ubuntu-${USER}" || true); do
            echo "Destroying leftover snapshot VM: $VM"
            virsh destroy "$VM" || true
        done

        if flock -n /tmp/edric-ci-snapshot.lock \
            su - "$USER" -c "cd ${USER_HOME}/_work/exe/exe && ./ops/ci-vm-snapshot.sh"; then
            echo "Snapshot creation succeeded"
        else
            echo "Snapshot creation failed or skipped (lock held)"
            # Clean up any VMs left by the failed attempt.
            for VM in $(virsh list --name 2>/dev/null | grep "^ci-ubuntu-${USER}" || true); do
                echo "Destroying snapshot VM after failure: $VM"
                virsh destroy "$VM" || true
            done
        fi
    fi

done

echo "=== $(date) === warmup complete ==="
