#!/usr/bin/env bash
# install-host-deps.sh — provision a fresh Ubuntu 24.04 host to run
# exe-ops-server. Installs: tailscale, git, ssh, build toolchain, Go.
#
# Usage (on the target host, as root):
#   curl -fsSL <raw-url>/install-host-deps.sh | sudo bash
# or after copying the file:
#   sudo ./install-host-deps.sh [--go-version=1.26.2]
#
# After this completes, bring the node up on Tailscale with the tag the
# server will filter on, e.g.:
#   sudo tailscale up --ssh --advertise-tags=tag:staging
#
# Then from a workstation, run:
#   ops/deploy-exe-ops-server.sh <host> <environment>
set -euo pipefail

GO_VERSION="1.26.2"

for arg in "$@"; do
    case "$arg" in
    --go-version=*) GO_VERSION="${arg#--go-version=}" ;;
    -h | --help)
        sed -n '2,20p' "$0"
        exit 0
        ;;
    *)
        echo "unknown argument: $arg" >&2
        exit 1
        ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: must run as root (use sudo)" >&2
    exit 1
fi

. /etc/os-release
if [ "${ID:-}" != "ubuntu" ] || [ "${VERSION_ID:-}" != "24.04" ]; then
    echo "WARNING: tested on Ubuntu 24.04; detected ${ID:-?} ${VERSION_ID:-?}. Continuing." >&2
fi

ARCH="$(dpkg --print-architecture)"
case "$ARCH" in
amd64) GO_ARCH="amd64" ;;
arm64) GO_ARCH="arm64" ;;
*)
    echo "ERROR: unsupported arch: $ARCH" >&2
    exit 1
    ;;
esac

echo "==> apt update + base packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    git \
    openssh-client \
    build-essential \
    pkg-config \
    jq \
    tar

echo "==> installing Node.js 24.x (NodeSource apt repo)"
# Needed so the UI Makefile's corepack-enable step (which has a
# '#!/usr/bin/env node' shebang) can find 'node' on PATH during bootstrap.
# The Makefile still downloads and uses its own pinned Node into .node/.
if ! command -v node >/dev/null 2>&1; then
    install -d -m 0755 /usr/share/keyrings
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key |
        gpg --dearmor -o /usr/share/keyrings/nodesource.gpg
    echo "deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_24.x nodistro main" \
        >/etc/apt/sources.list.d/nodesource.list
    apt-get update -y
    apt-get install -y nodejs
else
    echo "node already present: $(node --version)"
fi

echo "==> installing Tailscale (official apt repo)"
if ! command -v tailscale >/dev/null 2>&1; then
    install -d -m 0755 /usr/share/keyrings
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg \
        -o /usr/share/keyrings/tailscale-archive-keyring.gpg
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list \
        -o /etc/apt/sources.list.d/tailscale.list
    apt-get update -y
    apt-get install -y tailscale
    systemctl enable --now tailscaled
else
    echo "tailscale already present: $(tailscale version | head -1)"
fi

echo "==> installing Go ${GO_VERSION}"
GO_CURRENT=""
if [ -x /usr/local/go/bin/go ]; then
    GO_CURRENT="$(/usr/local/go/bin/go env GOVERSION 2>/dev/null | sed 's/^go//')"
fi
if [ "$GO_CURRENT" = "$GO_VERSION" ]; then
    echo "go ${GO_VERSION} already installed at /usr/local/go"
else
    TARBALL="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' EXIT
    echo "downloading ${TARBALL}"
    curl -fsSL -o "${TMPDIR}/${TARBALL}" "https://go.dev/dl/${TARBALL}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "${TMPDIR}/${TARBALL}"
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi
/usr/local/go/bin/go version

echo "==> creating /opt/exe-ops"
install -d -m 0755 /opt/exe-ops /opt/exe-ops/bin

cat <<'EOF'

==> Next steps:

1. Bring this node up on Tailscale with the right tag:
     sudo tailscale up --ssh --advertise-tags=tag:staging     # or tag:prod

2. Confirm HTTPS cert issuance works from this node:
     sudo tailscale cert $(hostname).<your-tailnet>.ts.net

3. Install a GitHub deploy key for exe repo access in /root/.ssh
   (the server clones git@github.com:boldsoftware/exe.git at startup):
     ssh-keyscan github.com >> /root/.ssh/known_hosts
     # then drop the private deploy key at /root/.ssh/id_ed25519

4. From a workstation, deploy the server:
     ops/deploy-exe-ops-server.sh <this-host> <environment>

EOF
