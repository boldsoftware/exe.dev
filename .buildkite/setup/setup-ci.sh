#!/bin/bash
# Idempotent Buildkite CI agent setup for an exe.dev CI host (Ubuntu 24.04, AMD).
# Targets the exe-ci-test queue. Run as the `ubuntu` user with passwordless sudo
# and a Buildkite cluster agent token in ~/token.
set -euo pipefail
trap 'echo "Error in $0 at line $LINENO" >&2' ERR

HOSTNAME_SHORT=$(hostname -s)
TOKEN=$(cat "$HOME/token")
QUEUE="${QUEUE:-exe-ci-test}"
SPAWN="${SPAWN:-24}"
GO_VERSION="${GO_VERSION:-1.26.2}"
DATA_DIR=/data
BK_HOME="$DATA_DIR/buildkite"
SOURCE_HOST="${SOURCE_HOST:-67.213.124.207}" # exe-ci-01, source for SSH keys
# Path to a local `exe` repo checkout on this host. Used to (a) build
# cloud-hypervisor host binaries from ops/cloud-hypervisor, (b) build
# psimon from cmd/psimon, and (c) source agent hooks. If unset, those
# steps are skipped — set EXE_REPO_REMOTE=/path/to/exe to enable them.
# Default tries a checkout in /tmp; the script clones one if missing.
EXE_REPO_REMOTE="${EXE_REPO_REMOTE:-/tmp/exe-for-ci-setup}"

say() { echo "==> $*"; }

# Make sure we have a repo checkout to build psimon and cloud-hypervisor from.
if [ ! -d "$EXE_REPO_REMOTE/.git" ]; then
    say "Cloning exe repo to $EXE_REPO_REMOTE for build steps"
    sudo rm -rf "$EXE_REPO_REMOTE"
    git clone --depth 1 "https://github.com/boldsoftware/exe.git" "$EXE_REPO_REMOTE" 2>/dev/null ||
        git clone --depth 1 "git@github.com:boldsoftware/exe.git" "$EXE_REPO_REMOTE" ||
        say "WARN: could not clone exe repo; will skip psimon + cloud-hypervisor build"
fi

# 0. /etc/hosts: add 127.0.1.1 <hostname> so `sudo` doesn't whine about resolution
if ! grep -q "127.0.1.1 ${HOSTNAME_SHORT}" /etc/hosts; then
    say "Adding ${HOSTNAME_SHORT} to /etc/hosts"
    echo "127.0.1.1 ${HOSTNAME_SHORT}" | sudo tee -a /etc/hosts >/dev/null
fi

# 1. Disable IPv6 (sysctl + grub)
say "Disabling IPv6"
SYSCTL_FILE=/etc/sysctl.d/99-disable-ipv6.conf
sudo tee "$SYSCTL_FILE" >/dev/null <<EOF
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
sudo sysctl --system >/dev/null
# Add ipv6.disable=1 to GRUB so it sticks across reboots.
if ! grep -q "ipv6.disable=1" /etc/default/grub; then
    sudo sed -i 's|GRUB_CMDLINE_LINUX_DEFAULT="|GRUB_CMDLINE_LINUX_DEFAULT="ipv6.disable=1 |' /etc/default/grub
    sudo update-grub >/dev/null
fi

# 2. /data — already mounted on md-raid0 ext4 by the provider image.
sudo mkdir -p "$BK_HOME"

# 3. apt packages
say "Installing apt packages"
export DEBIAN_FRONTEND=noninteractive
sudo apt-get update -qq
sudo apt-get install -y -qq \
    qemu-kvm libvirt-daemon-system libvirt-clients virtinst \
    bridge-utils cpu-checker cloud-image-utils genisoimage \
    virt-manager libguestfs-tools dnsmasq-base \
    build-essential make rsync jq zstd \
    libgbm1 libatk1.0-0 libatk-bridge2.0-0 \
    libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 \
    libxfixes3 libxrandr2 libpango-1.0-0 libcairo2 libasound2t64 \
    libnspr4 libnss3 netdata ca-certificates curl gnupg

# 4. Node 22 (Playwright)
if ! command -v node >/dev/null || ! node --version | grep -q ^v22; then
    say "Installing Node 22"
    curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash - >/dev/null
    sudo apt-get install -y -qq nodejs
fi

# 5. Docker
if ! command -v docker >/dev/null; then
    say "Installing Docker"
    curl -fsSL https://get.docker.com | sudo sh >/dev/null
fi

# 6. Go
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go${GO_VERSION}"; then
    say "Installing Go ${GO_VERSION}"
    TGZ="go${GO_VERSION}.linux-amd64.tar.gz"
    curl -sLO "https://go.dev/dl/${TGZ}"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "$TGZ"
    rm -f "$TGZ"
fi
echo 'export PATH="/usr/local/go/bin:$PATH"' | sudo tee /etc/profile.d/go.sh >/dev/null

# 7. libvirt: relocate images to /data, start default NAT
say "Configuring libvirt"
sudo systemctl enable --now libvirtd virtlogd >/dev/null
sudo mkdir -p /data/libvirt/images
if [ ! -L /var/lib/libvirt/images ]; then
    sudo systemctl stop libvirtd
    sudo mv /var/lib/libvirt/images/* /data/libvirt/images/ 2>/dev/null || true
    sudo rm -rf /var/lib/libvirt/images
    sudo ln -sfn /data/libvirt/images /var/lib/libvirt/images
    sudo systemctl start libvirtd
fi
sudo virsh net-start default 2>/dev/null || true
sudo virsh net-autostart default 2>/dev/null || true

# 8. Buildkite agent install (apt repo)
if ! command -v buildkite-agent >/dev/null; then
    say "Installing buildkite-agent"
    curl -fsSL https://keys.openpgp.org/vks/v1/by-fingerprint/32A37959C2FA5C3C99EFBC32A79206696452D198 |
        sudo gpg --dearmor -o /usr/share/keyrings/buildkite-agent-archive-keyring.gpg
    echo "deb [signed-by=/usr/share/keyrings/buildkite-agent-archive-keyring.gpg] https://apt.buildkite.com/buildkite-agent stable main" |
        sudo tee /etc/apt/sources.list.d/buildkite-agent.list >/dev/null
    sudo apt-get update -qq
    sudo apt-get install -y -qq buildkite-agent
fi

# 9. Move buildkite-agent home to /data/buildkite (idempotent)
if ! getent passwd buildkite-agent | grep -q ":${BK_HOME}:"; then
    say "Setting buildkite-agent home to ${BK_HOME}"
    sudo systemctl stop buildkite-agent || true
    sudo usermod -d "$BK_HOME" buildkite-agent
fi
sudo mkdir -p "$BK_HOME"/{builds,git-mirrors,checkout-cache,.cache,go,.ssh}
sudo chown -R buildkite-agent:buildkite-agent "$BK_HOME"

# 10. Group memberships
sudo usermod -aG libvirt buildkite-agent
sudo usermod -aG kvm buildkite-agent
sudo usermod -aG docker buildkite-agent

# 11. Sudoers
echo 'buildkite-agent ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/buildkite-ci >/dev/null
sudo chmod 0440 /etc/sudoers.d/buildkite-ci

# 12. Agent config + hooks (hooks are uploaded separately in /tmp/bk-hooks)
say "Writing /etc/buildkite-agent/buildkite-agent.cfg"
sudo tee /etc/buildkite-agent/buildkite-agent.cfg >/dev/null <<EOF
name="%hostname-%spawn"
spawn=${SPAWN}
token="${TOKEN}"
tags="queue=${QUEUE}"
build-path="${BK_HOME}/builds"
hooks-path="/etc/buildkite-agent/hooks"
plugins-path="/etc/buildkite-agent/plugins"
git-mirrors-path="${BK_HOME}/git-mirrors"
EOF
sudo chown root:buildkite-agent /etc/buildkite-agent/buildkite-agent.cfg
sudo chmod 0640 /etc/buildkite-agent/buildkite-agent.cfg

if [ -f /tmp/bk-hooks/environment ]; then
    sudo install -o root -g root -m 0755 /tmp/bk-hooks/environment /etc/buildkite-agent/hooks/environment
fi
if [ -f /tmp/bk-hooks/checkout ]; then
    sudo install -o root -g root -m 0755 /tmp/bk-hooks/checkout /etc/buildkite-agent/hooks/checkout
fi

# 13. SSH keys for github (uploaded separately to /tmp/bk-ssh)
if [ -d /tmp/bk-ssh ]; then
    say "Installing buildkite-agent SSH keys"
    sudo cp -n /tmp/bk-ssh/* "$BK_HOME/.ssh/" || true
    sudo chown -R buildkite-agent:buildkite-agent "$BK_HOME/.ssh"
    sudo chmod 700 "$BK_HOME/.ssh"
    sudo find "$BK_HOME/.ssh" -name 'id_*' ! -name '*.pub' -exec chmod 600 {} \;
    sudo -u buildkite-agent bash -c "ssh-keyscan github.com 2>/dev/null >> $BK_HOME/.ssh/known_hosts && sort -u $BK_HOME/.ssh/known_hosts -o $BK_HOME/.ssh/known_hosts"
fi

# 13b. cloud-hypervisor host binaries (cloud-hypervisor, virtiofsd, ch-remote).
# Built from the repo's ops/cloud-hypervisor Dockerfile if not already present.
# Required because the e1e jobs invoke cloud-hypervisor via setsid from PATH.
if ! command -v cloud-hypervisor >/dev/null && [ -d "$EXE_REPO_REMOTE/ops/cloud-hypervisor" ]; then
    say "Building cloud-hypervisor host binaries via Docker (~5 min)"
    CLOUD_HV_VERSION="${CLOUD_HV_VERSION:-48.0}"
    VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-1.13.2}"
    ARCH_DEB=$(dpkg --print-architecture) # amd64 / arm64
    PLATFORM="linux/${ARCH_DEB}"
    IMG="exe-cloud-hypervisor:${CLOUD_HV_VERSION}-${ARCH_DEB}"
    sudo docker build \
        --platform "$PLATFORM" \
        --tag "$IMG" \
        --build-arg "CLOUD_HYPERVISOR_VERSION=${CLOUD_HV_VERSION}" \
        --build-arg "VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}" \
        --build-arg "TARGETARCH=${ARCH_DEB}" \
        "$EXE_REPO_REMOTE/ops/cloud-hypervisor"
    CID=$(sudo docker create "$IMG" /bin/true)
    TMP=$(mktemp -d)
    sudo docker cp "${CID}:/out/." "$TMP"
    sudo install -m 0755 "$TMP/bin/cloud-hypervisor" /usr/local/bin/cloud-hypervisor
    sudo install -m 0755 "$TMP/bin/virtiofsd" /usr/local/bin/virtiofsd
    sudo install -m 0755 "$TMP/bin/ch-remote" /usr/local/bin/ch-remote
    # Also pre-populate the guest-side tarball cache so the snapshot step doesn't
    # have to rebuild on first run.
    sudo -u buildkite-agent mkdir -p "$BK_HOME/.cache/exedops"
    ART="$BK_HOME/.cache/exedops/cloud-hypervisor-${CLOUD_HV_VERSION}-${ARCH_DEB}.tar.gz"
    if [ ! -f "$ART" ]; then
        sudo tar -C "$TMP" -czf "$ART" .
        sudo chown buildkite-agent:buildkite-agent "$ART"
    fi
    sudo rm -rf "$TMP"
    sudo docker rm "$CID" >/dev/null
fi

# 13c. psimon (CI machine pressure monitor). Build from the repo and install
# the systemd unit. The collect-psimon step in the pipeline queries it.
if [ -d "$EXE_REPO_REMOTE/cmd/psimon" ] && [ ! -x /usr/local/bin/psimon ]; then
    say "Building and installing psimon"
    (cd "$EXE_REPO_REMOTE" && /usr/local/go/bin/go build -o /tmp/psimon ./cmd/psimon/)
    sudo install -m 0755 /tmp/psimon /usr/local/bin/psimon
    sudo install -m 0644 "$EXE_REPO_REMOTE/cmd/psimon/psimon.service" /etc/systemd/system/psimon.service
    sudo systemctl daemon-reload
    sudo systemctl enable --now psimon
    rm -f /tmp/psimon
fi

# 13d. Docker Hub credentials for the buildkite-agent. Required: `make protos`
# and the snapshot step pull base images from Docker Hub and rate-limits
# unauthenticated pulls. The setup script does NOT bake credentials in; provide
# /tmp/docker-config.json (a copy of an existing host's config.json) and we'll
# install it.
if [ -f /tmp/docker-config.json ] && [ ! -f "$BK_HOME/.docker/config.json" ]; then
    sudo install -d -o buildkite-agent -g buildkite-agent -m 0700 "$BK_HOME/.docker"
    sudo install -o buildkite-agent -g buildkite-agent -m 0600 \
        /tmp/docker-config.json "$BK_HOME/.docker/config.json"
fi

# 14. uv + b2 for buildkite-agent (used by exelet-fs download in test scripts)
if [ ! -x "$BK_HOME/.local/bin/uv" ]; then
    say "Installing uv for buildkite-agent"
    sudo -u buildkite-agent bash -c 'curl -LsSf https://astral.sh/uv/install.sh | sh' >/dev/null
fi
sudo -u buildkite-agent bash -c "source $BK_HOME/.local/bin/env && uv tool install --quiet b2 2>&1" >/dev/null || true

# 15. Start agent
say "Starting buildkite-agent"
sudo systemctl daemon-reload
sudo systemctl enable --now buildkite-agent
sleep 2
sudo systemctl status buildkite-agent --no-pager 2>&1 | head -10

say "Done. ls -la /dev/kvm:"
ls -la /dev/kvm
