#!/usr/bin/env bash
# One-time setup of /data/e1ed/push.git on edric.
# Run this script ON edric (ssh root@edric).
set -euo pipefail

echo "Setting up /data/e1ed/push.git..."

git init --bare /data/e1ed/push.git

# Make pushed objects visible to the e1ed repo via alternates.
echo "/data/e1ed/push.git/objects" >>/data/e1ed/repo.git/objects/info/alternates

# Disable GC on push.git to prevent object pruning.
git -C /data/e1ed/push.git config gc.auto 0

mkdir -p /data/e1ed/runs

# Install post-receive hook.
cat >/data/e1ed/push.git/hooks/post-receive <<'EOF'
#!/bin/bash
exec /usr/local/bin/e1ed-hook
EOF
chmod +x /data/e1ed/push.git/hooks/post-receive

echo "Done. Add remote: git remote add edric root@edric:/data/e1ed/push.git"
