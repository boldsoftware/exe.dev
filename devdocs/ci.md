# CI Infrastructure

## Machines

### edric (`ssh root@edric`)

Primary CI machine. Ubuntu 24.04, 64 CPUs, 503GB RAM, 4x 3.6TB NVMe.

**Runners:**

| Name | Labels | Purpose |
|------|--------|---------|
| `edric-0` .. `edric-7` | `self-hosted, Linux, X64, libvirt, edric` | e1e tests (VM-based) |
| `edric-ci-0` .. `edric-ci-7` | `self-hosted, Linux, X64, exe-ci, edric` | non-e1e tests, shelley tests |

All 16 runners are org-level (registered under `boldsoftware`, not a specific repo).

**Users:** `runner0` through `runner7`. Each user runs two runner services:
- `~/actions-runner/` → e1e runner (`edric-N`)
- `~/actions-runner-ci/` → non-e1e runner (`edric-ci-N`)

**Systemd services:** All enabled and auto-start on boot.
```
actions.runner.boldsoftware.edric-{0..7}
actions.runner.boldsoftware.edric-ci-{0..7}
```

### ci.bold.dev (`ssh root@ci.bold.dev`)

Original CI machine. Runs the existing `exe-ci` and `libvirt` runners.

## Shelley Tests

Shelley tests run on the `edric-ci-*` runners (same pool as non-e1e tests). The shelley-tests.yml workflow uses `runs-on: [self-hosted, exe-ci]`.

**Installed software for shelley:**
- Google Chrome (`google-chrome-stable`) — needed by Go browse tests (chromedp)
- pnpm 10 (global) — used by warmup; CI also installs via `pnpm/action-setup`
- Node.js and Go versions are managed per-run by `actions/setup-node` and `actions/setup-go`

Playwright E2E tests (`run_playwright: true`) are skipped in the commit queue but run in the standalone shelley-tests.yml workflow. The `npx playwright install --with-deps chromium` step installs the Playwright-specific chromium binary; the Google Chrome system deps satisfy the shared library requirements.

## Targeting Edric

All edric runners have the `edric` label. To force a CI run onto edric (e.g. for testing), add `edric` to the `runs-on` array:

```yaml
runs-on: [self-hosted, exe-ci, edric]  # only edric CI runners
runs-on: [self-hosted, libvirt, edric]  # only edric e1e runners
```

## How e1e Test Isolation Works

Each e1e runner creates libvirt VMs for testing. Without isolation, the "destroy stale VMs" step would kill other runners' active VMs.

**Solution:** The `E1E_VM_PREFIX` environment variable controls the VM name prefix. Each runner's systemd service sets a unique prefix:

```
edric-0: E1E_VM_PREFIX=e1e-runner0  →  VMs named e1e-runner0-{testid}-{timestamp}
edric-1: E1E_VM_PREFIX=e1e-runner1  →  VMs named e1e-runner1-{testid}-{timestamp}
...
```

The cleanup step in CI only destroys VMs matching the current runner's prefix:
```bash
sudo virsh list --name | grep -E "^${PREFIX}" | xargs -r -n 1 sudo virsh destroy
```

This is implemented in `e1e/testinfra/vm.go`. If `E1E_VM_PREFIX` is unset, it defaults to `ci-ubuntu` for backward compatibility.

Similarly, `ops/ci-vm-env.sh` includes `$(whoami)` in the default VM name so that snapshot creation VMs don't collide between concurrent runners.

## `/tmp` Isolation

The **non-e1e** runner services (`edric-ci-*`) have `PrivateTmp=true`, which gives each runner its own private `/tmp` namespace. This prevents collisions on hardcoded `/tmp/` paths (e.g. `/tmp/exelint`, `/tmp/exelet-fs/`) between concurrent runners without needing to change any code.

The **e1e** runner services (`edric-*`) do **not** use `PrivateTmp` because e1e tests use `os.Rename` to move the exelet binary into `/tmp`, which fails across filesystem boundaries. E1e `/tmp` paths are already isolated by the random `testRunID` suffix (e.g. `/tmp/exelet-test-{testRunID}`).

If you add a new non-e1e runner service, include `PrivateTmp=true` in the `[Service]` section.

## Disk Layout

VM disk images are on a 4-drive NVMe RAID0 for I/O throughput:
```
/var/lib/libvirt/images → /data/libvirt/images  (symlink)
/data is a 12.6TB ext4 on md0 (RAID0 across 4 NVMe drives)
```

This is important: with 8 concurrent VMs all doing ZFS operations, a single NVMe drive bottlenecks on zvol cloning. The RAID0 provides ~4x throughput.

Image types in `/var/lib/libvirt/images/`:
- `ubuntu-24.04-base.qcow2` — base cloud image (downloaded once)
- `ci-base-{hash}.qcow2` — provisioned snapshot with Cloud Hypervisor + exelet
- `ci-data-{hash}.qcow2` — provisioned ZFS data disk snapshot
- `e1e-runnerN-{testid}-{timestamp}.qcow2` — per-test overlay (copy-on-write from snapshot)

## VM Snapshot Cache

Snapshots are cached per-user at `$HOME/.cache/exedev/ci-vm-{hash}-{date}/`. The cache key includes:
- `ops/` git tree hash (changes when provisioning scripts change)
- `exeuntu` container image digest (changes when the base image updates)
- Date in `YYYYMMDD` format (rotates daily)

When the cache is cold, `ci-vm-snapshot.sh` creates a VM, lets it fully provision, then saves the disk state as a snapshot. Subsequent test runs create lightweight copy-on-write overlays from this snapshot.

## Cron Jobs

Defined in `/etc/cron.d/edric-ci` on edric:

**Warmup** (`/usr/local/bin/edric-ci-warmup.sh`): Runs every 5 minutes during working hours (6am ET – 9pm PT). Pre-warms:
- Git prefetch via SSH deploy key (fetches to `refs/prefetch/`, see below)
- Go module cache (`go mod download`) for both main and shelley modules
- Go build cache (`go build ./...`) for both main and shelley modules
- `exelet-fs` download (`make exelet-fs`)
- Shelley UI dependencies (`pnpm install` in `shelley/ui/`)
- VM snapshot creation (for the new day's cache key)
- Snapshot cache propagation from runner0 to runner1-7

Almost always a no-op (all checks are idempotent). Only does real work when code changes or the date rolls over.

**Git maintenance**: Each runner workdir has `git maintenance start` enabled (per-user crontab). This handles local repo housekeeping (gc, loose-objects, incremental-repack) on an hourly/daily/weekly schedule. The `prefetch` task is handled by the warmup script instead (see below) because it needs SSH credentials.

**Cleanup** (`/usr/local/bin/edric-ci-cleanup.sh`): Runs every 15 minutes. Cleans up:
- Stale VMs running longer than 30 minutes
- Orphaned disk images with no matching running VM
- Old snapshot caches (>2 days)

## Git Prefetch

The warmup script pre-fetches git objects so that `actions/checkout` is fast. This is done carefully to avoid collisions:

- **Problem**: A naive `git fetch origin` in the warmup cron would update `refs/remotes/origin/*` at the same time as `actions/checkout`, causing ref lock errors (`cannot lock ref`).
- **Solution**: The warmup fetches to `refs/prefetch/remotes/origin/*` instead. Git objects are shared across all refs, so when `actions/checkout` later does its real fetch, the objects are already local and only ref pointers need updating.

The prefetch uses a read-only SSH deploy key at `/etc/edric-ci-deploy-key` (the repo is private, so HTTPS without credentials won't work). The key is owned by `root:ci-runners` with mode `640`, readable by all runner users via the `ci-runners` group.

## Debugging

### Check runner status
```bash
ssh root@edric
for svc in $(systemctl list-units --type=service --no-legend | grep actions.runner | awk '{print $1}'); do
  echo -n "$svc: "; systemctl is-active "$svc"
done
```

### Check running VMs
```bash
ssh root@edric sudo virsh list --all
```

### Check resource usage during concurrent runs
```bash
ssh root@edric 'uptime && free -h && sudo virsh list'
```

### View runner logs
```bash
ssh root@edric sudo journalctl -u actions.runner.boldsoftware.edric-0 --since "1 hour ago" --no-pager
```

### View warmup/cleanup logs
```bash
ssh root@edric tail -50 /var/log/edric-ci-warmup.log
ssh root@edric tail -50 /var/log/edric-ci-cleanup.log
```

### Manually destroy all VMs for a runner
```bash
ssh root@edric 'sudo virsh list --name | grep e1e-runner0 | xargs -r -n 1 sudo virsh destroy'
```

### Restart a runner service
```bash
ssh root@edric sudo systemctl restart actions.runner.boldsoftware.edric-0
```

## Capacity

Tested with 8 concurrent e1e test suites (8 VMs, each 4 vCPUs / 16GB RAM):
- CPU: ~35% utilization (32/64 cores)
- Memory: ~15% utilization (78GB/503GB)
- Disk I/O: comfortable on RAID0
- All 8 suites pass in ~2m30s each (vs 3m15s single-runner baseline)

The machine can comfortably handle 8 concurrent e1e runners + 8 concurrent non-e1e runners simultaneously.

## After a Reboot

Everything comes back automatically:
- All 16 systemd runner services are enabled
- libvirtd is enabled
- `/data` RAID0 is in fstab
- Symlink `/var/lib/libvirt/images → /data/libvirt/images` persists

The first CI run after reboot will be slightly slower (VM snapshot needs to be recreated, Go build cache is cold). The cron warmup will start re-populating caches within 5 minutes.
