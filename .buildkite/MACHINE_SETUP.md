# exe-ci-01 Buildkite Agent Setup Notes

This document describes how `exe-ci-01` was configured as a Buildkite CI agent
for the `exe` repo. Recreating it from a fresh Ubuntu 24.04 machine requires
these steps.

## Machine specs

- AMD EPYC 9254 24-Core (48 threads)
- 377 GiB RAM
- 2× 447 GB NVMe (OS on nvme0, nvme1 spare)
- 3× 3.5 TB NVMe in ZFS pool `tank` (7.4 TiB usable)
- `/dev/kvm` present — nested KVM works (host is VMware)

## 1. ZFS pool

The three large NVMe drives were already in a ZFS pool named `tank` with
`mountpoint=none`. Mount it at `/data`:

```bash
sudo zfs set mountpoint=/data tank
sudo zfs mount tank
```

Create the libvirt image store on fast ZFS storage and symlink it:

```bash
sudo zfs create tank/libvirt
sudo zfs create tank/libvirt/images
sudo systemctl stop libvirtd
sudo mv /var/lib/libvirt/images/* /data/libvirt/images/ 2>/dev/null || true
sudo rmdir /var/lib/libvirt/images
sudo ln -sf /data/libvirt/images /var/lib/libvirt/images
sudo systemctl start libvirtd
```

CI snapshot cache lives at `~/.cache/ci-snapshots` (override with `EXEDEV_CACHE` env var).

## 2. Package installation

```bash
# Virtualization stack
sudo apt-get install -y \
  qemu-kvm libvirt-daemon-system libvirt-clients virtinst \
  bridge-utils cpu-checker cloud-image-utils genisoimage \
  virt-manager libguestfs-tools dnsmasq-base

# Build tools
sudo apt-get install -y build-essential make

# Node.js 22 (for Playwright)
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo bash -
sudo apt-get install -y nodejs

# Playwright browser system dependencies
sudo npx playwright install-deps chromium
# Also install directly:
sudo apt-get install -y libgbm1 libatk1.0-0 libatk-bridge2.0-0 \
  libcups2 libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 \
  libxfixes3 libxrandr2 libpango-1.0-0 libcairo2 libasound2t64 \
  libnspr4 libnss3

# Docker
curl -fsSL https://get.docker.com | sudo sh
```

## 3. Go 1.26

```bash
curl -sLO https://go.dev/dl/go1.26.1.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
```

Add to `/etc/profile.d/go.sh`:

```bash
export PATH="/usr/local/go/bin:$PATH"
```

## 4. Buildkite agent

Install via the official script or package repo. The agent user is
`buildkite-agent` with home `/var/lib/buildkite-agent`.

### Cluster and queue

The agent must register to a **self-hosted queue** (not the default hosted
queue). In the Buildkite UI under Organization → Clusters → Default cluster:

1. Create a queue named `exe-ci` (self-hosted, not Buildkite-hosted).
2. Create a cluster token (or use an existing one scoped to that cluster).

`/etc/buildkite-agent/buildkite-agent.cfg`:

```
name="%hostname-%spawn"
spawn=16
token="bkct_..."   # cluster token from step above
tags="queue=exe-ci"
build-path="/var/lib/buildkite-agent/builds"
hooks-path="/etc/buildkite-agent/hooks"
plugins-path="/etc/buildkite-agent/plugins"
```

`spawn=16` runs 16 agent processes — covers: 1 pipeline-upload + 1 checks +
2 unit-test shards + 6 e1e shards (A–F) + 1 exelets + spare headroom.

### Environment hook

`/etc/buildkite-agent/hooks/environment`:

```bash
#!/bin/bash
export HOME=/data/buildkite
export GOPATH="$HOME/go"
export PATH="/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin:$HOME/.local/bin:$HOME/go/bin"
# CI caches default to ~/.cache/ subdirectories. No CI_CACHE or EXEDEV_CACHE override needed
# since HOME is on the ZFS data volume.

# SSH multiplexing for GitHub. The buildkite agent makes 3 SSH connections to
# github.com per checkout (mirror fetch, clone, working-copy fetch). With
# ControlMaster, only the first pays the SSH handshake cost; the rest reuse
# the connection. With spawn=16 agents, the shared socket path means most
# checkouts hit an already-warm connection. Saves ~2s per build.
export GIT_SSH_COMMAND="ssh -o ControlMaster=auto -o ControlPath=/tmp/ssh-mux-buildkite-%r@%h:%p -o ControlPersist=300 -o ServerAliveInterval=60"
```

```bash
sudo chmod +x /etc/buildkite-agent/hooks/environment
```

### Group memberships

```bash
sudo usermod -aG libvirt buildkite-agent
sudo usermod -aG kvm     buildkite-agent
sudo usermod -aG docker  buildkite-agent
```

Restart the agent after group changes so they take effect:

```bash
sudo systemctl restart buildkite-agent
```

### sudo access

The CI scripts (`ops/ci-vm-start.sh` etc.) use `sudo` heavily for virsh,
qemu-img, ZFS, Docker, and network setup. Give `buildkite-agent` full
passwordless sudo:

`/etc/sudoers.d/buildkite-ci`:

```
buildkite-agent ALL=(ALL) NOPASSWD: ALL
```

## 5. SSH / GitHub access

The `buildkite-agent` user needs to clone the private `exe` repo from GitHub.
Copy the deploy key:

```bash
sudo mkdir -p /var/lib/buildkite-agent/.ssh
sudo cp ~/.ssh/id_rsa /var/lib/buildkite-agent/.ssh/id_rsa
sudo chown -R buildkite-agent:buildkite-agent /var/lib/buildkite-agent/.ssh
sudo chmod 600 /var/lib/buildkite-agent/.ssh/id_rsa
sudo -u buildkite-agent ssh-keyscan github.com >> /var/lib/buildkite-agent/.ssh/known_hosts
```

Verify:

```bash
sudo -u buildkite-agent ssh -T git@github.com
# → Hi boldsoftware/exe! You've successfully authenticated...
```

Also generate an ed25519 key (used by `ops/ci-vm-env.sh` as `SSH_PUBKEY`):

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N "" -C "exe-ci-01"
sudo cp ~/.ssh/id_ed25519 /var/lib/buildkite-agent/.ssh/id_ed25519
sudo chown buildkite-agent:buildkite-agent /var/lib/buildkite-agent/.ssh/id_ed25519
```

## 6. uv and b2 CLI

The test scripts download `exelet-fs` from Backblaze using the `b2` CLI.

```bash
# For the ubuntu user
curl -LsSf https://astral.sh/uv/install.sh | sh
uv tool install b2

# For the buildkite-agent user
sudo -u buildkite-agent bash -c 'curl -LsSf https://astral.sh/uv/install.sh | sh'
sudo -u buildkite-agent bash -c 'source ~/.local/bin/env && uv tool install b2'
```

## 7. libvirt / KVM setup

Start and enable libvirtd, start the default NAT network:

```bash
sudo systemctl enable --now libvirtd virtlogd
sudo virsh net-start default
sudo virsh net-autostart default
```

The default NAT network (`virbr0`) requires `dnsmasq-base` (installed above).

## 8. Cloud Hypervisor artifact cache

`ci-vm-start.sh` builds Cloud Hypervisor binaries via Docker on first run, then
caches them in `$HOME/.cache/exedops/`. To pre-populate the cache for
`buildkite-agent` (avoiding a Docker root-permission issue with `rm -rf` on
container-owned files):

```bash
CLOUD_HV_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"
CACHE_DIR="/var/lib/buildkite-agent/.cache/exedops"
sudo -u buildkite-agent mkdir -p "$CACHE_DIR"

sudo docker build \
  --platform linux/amd64 \
  --tag "exe-cloud-hypervisor:${CLOUD_HV_VERSION}-amd64" \
  --build-arg "CLOUD_HYPERVISOR_VERSION=${CLOUD_HV_VERSION}" \
  --build-arg "VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}" \
  --build-arg "TARGETARCH=amd64" \
  "./ops/cloud-hypervisor"

CONTAINER_ID=$(sudo docker create "exe-cloud-hypervisor:${CLOUD_HV_VERSION}-amd64" /bin/true)
TMP_DIR=$(mktemp -d)
sudo docker cp "${CONTAINER_ID}:/out/." "${TMP_DIR}"
sudo tar czf "${CACHE_DIR}/cloud-hypervisor-${CLOUD_HV_VERSION}-amd64.tar.gz" -C "${TMP_DIR}" .
sudo chmod 0644 "${CACHE_DIR}/cloud-hypervisor-${CLOUD_HV_VERSION}-amd64.tar.gz"
sudo chown buildkite-agent:buildkite-agent "${CACHE_DIR}/cloud-hypervisor-${CLOUD_HV_VERSION}-amd64.tar.gz"
sudo rm -rf "${TMP_DIR}"
sudo docker rm "${CONTAINER_ID}"
```

This step needs to be repeated when `CLOUD_HYPERVISOR_VERSION` or
`VIRTIOFSD_VERSION` in `ops/ci-vm-env.sh` changes, or you can just let the
agent do it (it will succeed once the agent runs as a user with docker group
access, since it uses `sudo rm`).

## 9. Buildkite pipeline

The pipeline definition lives in `.buildkite/pipeline.yml` (in this repo).
The Buildkite pipeline was created via the API pointing at
`git@github.com:boldsoftware/exe.git` in the `bold-software` organization,
cluster `Default cluster`, with the initial step set to
`buildkite-agent pipeline upload .buildkite/pipeline.yml` targeting
`queue=exe-ci`.

To recreate the pipeline via API:

```bash
curl -X POST \
  -H "Authorization: Bearer $BUILDKITE_API_KEY" \
  -H "Content-Type: application/json" \
  "https://api.buildkite.com/v2/organizations/bold-software/pipelines" \
  -d '{
    "name": "exe e1e tests",
    "repository": "git@github.com:boldsoftware/exe.git",
    "cluster_id": "0adccc8f-4c01-4b5c-b9c3-1dcb76fd44c6",
    "default_branch": "main"
  }'
# Then PATCH the configuration field with the YAML upload step targeting queue=exe-ci
```

## 10. Secrets

`STRIPE_SECRET_KEY` (Stripe test-mode key) enables ~14 additional billing
tests. Set it as a Buildkite secret in the pipeline or cluster, then reference
it in the step env. Tests skip gracefully if it is absent.

## 11. Monitoring (netdata)

```bash
sudo apt-get install -y netdata
```

The default config (`/etc/netdata/netdata.conf`) binds to `127.0.0.1:19999`,
so the dashboard is only accessible from localhost. No changes needed.

## Notes / gotchas

- **Polling mode**: The agent falls back to polling (not streaming) — this is
  normal; Buildkite streaming requires a specific enterprise feature.
- **`make protos`** runs Docker inside the CI job. The `buildkite-agent` user
  needs to be in the `docker` group *and* the agent must be restarted after
  adding the group.
- **VM concurrency**: The pipeline generator sets `E1E_VM_CONCURRENCY=12` per
  e1e shard (5 shards by default). The exelets step runs with 10 VMs. Each VM
  slot uses ~2–4 vCPUs and ~4 GiB RAM; with 5 shards × 12 VMs + exelets × 10,
  peak concurrent VMs can reach ~70 but are staggered in practice so this
  stays within the machine's 48 vCPUs / 377 GiB capacity. Tune via commit
  trailers (E1E-Shards, E1E-VM-Concurrency) or env vars.
- **Golden files**: If e1e tests modify golden files, the job fails with a diff.
  On GitHub Actions the diff is pushed to a recovery branch. On Buildkite, just
  run the tests locally, commit the updated golden files, and re-push.
- **VM snapshot freshness**: Snapshots are keyed by `date +%Y%m%d` + ops/ tree
  hash + exeuntu image digest. They regenerate daily. The first run of the day
  takes longer (VM boot + setup); subsequent runs reuse the snapshot.
