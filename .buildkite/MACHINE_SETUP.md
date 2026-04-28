# Buildkite Agent Setup Notes

This document describes how the `exe-ci-*` machines were configured as Buildkite
CI agents for the `exe` repo. Recreating from a fresh Ubuntu 24.04 machine
requires the steps below.

There is an idempotent setup script at `.buildkite/setup-ci.sh` (run as
`ubuntu` with passwordless sudo and a Buildkite cluster agent token in
`~/token`) that performs steps 1–14 below.

## Pre-flight: BIOS

**AMD-V / SVM must be enabled in the BIOS** on AMD bare-metal hosts (Latitude,
etc.). Symptom of it being disabled: `/dev/kvm` is missing and `dmesg | grep
svm` shows `SVM disabled (by BIOS) in MSR_VM_CR`. This blocks every e1e test
(they all spawn cloud-hypervisor VMs). On Latitude this can be flipped from
the provider portal; sometimes a reinstall changes the host key, so reset
your `~/.ssh/known_hosts` entry afterwards.

## Machine specs

### exe-ci-01 (VMware guest)
- AMD EPYC 9254 24-Core (48 threads)
- 377 GiB RAM
- 2× 447 GB NVMe (OS on nvme0, nvme1 spare)
- 3× 3.5 TB NVMe in ZFS pool `tank` (7.4 TiB usable)
- `/dev/kvm` present — nested KVM works (host is VMware)
- 16 spawn agents on `queue=exe-ci`

### exe-ci-03 (bare-metal Latitude)
- AMD EPYC 9455 48-Core (48 threads, no SMT)
- 755 GiB RAM
- 2× 447 GB NVMe in md-raid1 mirror → `/`
- 2× 3.5 TB NVMe in md-raid0 stripe → `/data` (ext4, 7 TiB usable)
- `/dev/kvm` present (after enabling SVM in BIOS — see pre-flight)
- 24 spawn agents on `queue=exe-ci-test` while shaking down
- Note: this host runs ext4 on md-raid, not ZFS. Anything that wanted
  `zfs set mountpoint=...` is a no-op; `/data` is the provider-mounted ext4.

## 0. Disable IPv6 (recommended on bare-metal)

No CI step needs IPv6, and on some Latitude hosts IPv6 makes systemd-resolved
delay startup. Disable at runtime + boot:

```bash
cat <<'EOF' | sudo tee /etc/sysctl.d/99-disable-ipv6.conf
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
sudo sysctl --system
sudo sed -i 's|GRUB_CMDLINE_LINUX_DEFAULT="|GRUB_CMDLINE_LINUX_DEFAULT="ipv6.disable=1 |' /etc/default/grub
sudo update-grub
```

## 1. Storage

On exe-ci-01 (ZFS pool `tank` from VMware-provisioned disks):

```bash
sudo zfs set mountpoint=/data tank
sudo zfs mount tank
```

On exe-ci-03 (md-raid0 ext4): the provider already mounts `/data`, nothing to do.

Create the libvirt image store on the fast volume and symlink it:

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
curl -sLO https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
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
# The hostname=<host> tag pins all jobs of a single build to one machine.
# generate-pipeline.py reads BUILDKITE_AGENT_META_DATA_HOSTNAME and emits
# `agents.hostname: <host>` so subsequent jobs route to the same host.
# Replace exe-ci-01 below with the actual hostname for this machine.
tags="queue=exe-ci,hostname=exe-ci-01"
build-path="/var/lib/buildkite-agent/builds"
hooks-path="/etc/buildkite-agent/hooks"
plugins-path="/etc/buildkite-agent/plugins"
```

`spawn=16` runs 16 agent processes on the 48-core exe-ci-01 — covers:
1 pipeline-upload + 1 checks + 2 unit-test shards + 6 e1e shards (A–F) +
1 exelets + spare headroom. On exe-ci-03 (also 48 cores, but 755 GiB RAM and
faster NVMe) we run `spawn=24` while shaking down, since several builds may
overlap before promotion.

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

## 8b. cloud-hypervisor host binaries

The e1e jobs invoke `cloud-hypervisor` directly from PATH (via `setsid`).
The setup script builds it from `ops/cloud-hypervisor/Dockerfile` (~5 min the
first time) and installs it to `/usr/local/bin/{cloud-hypervisor,virtiofsd,ch-remote}`,
plus pre-populates `~/.cache/exedops/cloud-hypervisor-<ver>-<arch>.tar.gz` so
the first snapshot job doesn't have to rebuild.

## 8c. Docker Hub credentials (the only secret you need to provide)

The rootfs-snapshot step (`ensure-snapshot`) requires `~/.docker/config.json`
for the buildkite-agent user (Docker Hub auth, to avoid rate limits). The
setup script does NOT bake the credentials in. Drop a copy at
`/tmp/docker-config.json` before running the script and it will install it
at `/data/buildkite/.docker/config.json` with the right perms. From an
existing CI host:

```bash
ssh ci-host 'sudo cat /data/buildkite/.docker/config.json' > /tmp/docker-config.json
scp /tmp/docker-config.json new-host:/tmp/docker-config.json
```

## 8d. psimon (machine pressure monitor)

The `collect-psimon` step in the pipeline queries `http://localhost:9101`.
The setup script builds psimon from `cmd/psimon` and installs it as a systemd
service (unit file at `cmd/psimon/psimon.service`). Verify with
`systemctl status psimon` and `curl localhost:9101/health`.

## 8e. Caches (auto-bootstrapped)

Everything else — Go module cache, UI build cache, exelet-fs tarballs,
Playwright browsers — is auto-populated by the build steps themselves on
first run. The first build of the day will be slower (~10–12 min) than
warm runs (~4–6 min). To skip that warm-up, you may copy `/data/buildkite/.cache/`
selectively from an existing CI host, but it's not required.

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

## 10b. Firewall (ufw)

Latitude bare-metal hosts have public IPs and are not firewalled by default.
Lock down inbound to SSH only (use the `OpenSSH` profile, not `22/tcp`, so
your SSH session reliably stays up across `ufw enable`):

```bash
sudo apt-get install -y ufw
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow OpenSSH
sudo ufw enable
sudo ufw status verbose
```

## 11. Monitoring (netdata)

```bash
sudo apt-get install -y netdata
```

The default config (`/etc/netdata/netdata.conf`) binds to `127.0.0.1:19999`,
so the dashboard is only accessible from localhost. No changes needed.

## Bringing a new host onto its own queue (shake-down)

While shaking down a new host, point it at a separate Buildkite queue
(e.g. `exe-ci-test`) so production traffic stays on `exe-ci`:

1. Create the queue under Organization → Clusters → Default cluster.
2. Create a cluster agent token. Buildkite's REST API does not let you
   restrict a token to a queue, but the agent's `tags="queue=..."` is a
   tighter contract than the queue-allowlist would be: the agent will
   only register with that tag, and other queues' steps will not match.
3. Set the agent's `tags="queue=exe-ci-test,hostname=<new-host>"` in
   `buildkite-agent.cfg` and restart the agent.
4. Send individual builds to the staging queue with the `CI-Queue:`
   commit trailer — no pipeline-config patch needed:
   ```
   ci: poke at exe-ci-03

   CI-Queue: exe-ci-test
   ```
   `generate-pipeline.py` reads the trailer and emits the right
   `agents.queue` for all child steps. When `CI-Queue` is set, the
   per-host pinning is skipped (the generate step ran on `exe-ci`,
   so we can't pin to that host for `exe-ci-test` jobs).
5. Push a `kite-test-*` branch via `bin/t --dry-run` and iterate.
6. When green, switch the agent's tag to `queue=exe-ci,hostname=<host>`
   and drop the `CI-Queue` trailer.

## Notes / gotchas

- **First-clone bug in the checkout hook**: when `BUILDKITE_BUILD_CHECKOUT_PATH`
  doesn't exist yet, the agent `cd`'s into a freshly-created empty checkout
  directory before invoking the hook. The hook then `rm -rf`'s its own cwd,
  which makes `getcwd()` fail in the next `git clone`. The hook now `cd /`
  first; see commit history of `.buildkite/agent/hooks/checkout`.
- **`pipeline upload --async`**: this flag does not exist in any released
  buildkite-agent (3.124 confirmed). A previous commit added it to
  `pipeline.yml` based on a misreading of the help; jobs that landed on it
  failed with `flag provided but not defined: -async`. Removed.
- **ui/Makefile PATH bug** (fixed): `export PATH := $(CURDIR)/$(NODE_BIN):$(PATH)`
  used to produce `ui//absolute/path/...` when `NODE_BIN` was absolute.
  Now the makefile branches on whether `CI_CACHE` is set and only prepends
  `$(CURDIR)/` when `NODE_BIN` is relative.
- **Playwright `text file busy`** (fixed): parallel e1e shards racing to
  install Playwright into a shared `~/.cache/ms-playwright-go/<ver>/node`
  triggered ETXTBSY on Linux. `e1e/testinfra/playwright.go` now takes a
  host-wide `flock` around `playwright.Install()` so concurrent shards
  serialize.
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
