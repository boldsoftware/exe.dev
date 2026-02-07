# Edric CI Runner Infrastructure

`ssh root@edric` — 64 CPUs, 503GB RAM, 4x NVMe RAID0 at `/data`.

## Runners

16 GitHub Actions runners registered under `boldsoftware` org:

| Runners | Labels | Purpose |
|---------|--------|---------|
| `edric-0` .. `edric-7` | `self-hosted, Linux, X64, libvirt, edric` | e1e tests (VM-based) |
| `edric-ci-0` .. `edric-ci-7` | `self-hosted, Linux, X64, exe-ci, edric` | non-e1e tests, shelley tests |

Each pair shares a user: `runner0` runs `edric-0` (e1e) and `edric-ci-0` (ci).

## Files in this directory

| File | Deploys to |
|------|-----------|
| `edric-e1e.service` | `/etc/systemd/system/actions.runner.boldsoftware.edric-N.service` |
| `edric-ci.service` | `/etc/systemd/system/actions.runner.boldsoftware.edric-ci-N.service` |
| `edric-ci-warmup.sh` | `/usr/local/bin/edric-ci-warmup.sh` |
| `edric-ci-cleanup.sh` | `/usr/local/bin/edric-ci-cleanup.sh` |
| `edric-ci.cron` | `/etc/cron.d/edric-ci` |
| `deploy-edric.sh` | (run locally to deploy everything above) |

Service files use `%i` as placeholder for runner number (0-7). The deploy script substitutes and installs 16 concrete service files.

## Deploying

```bash
./ops/ci/deploy-edric.sh
```

This installs Go, deploys service files/scripts/cron, ensures groups and sudoers, reloads systemd, restarts all runners, and verifies.

## Key design decisions

- **E1e services set explicit PATH** including `/usr/local/go/bin`. The runner can rewrite its `.path` file during auto-updates, losing Go from PATH. The explicit systemd PATH prevents this.
- **E1e services do NOT use PrivateTmp** because e1e tests use `os.Rename` across `/tmp` boundaries. Isolation comes from the random `testRunID` suffix in `/tmp` paths.
- **CI services use PrivateTmp=true** to isolate hardcoded `/tmp/` paths between concurrent runners.
- **E1E_VM_PREFIX** ensures each e1e runner's VMs have unique names, so the cleanup step only destroys its own VMs.

## Warmup cron

Runs every 5 minutes during working hours. Pre-warms: git objects (via prefetch to avoid ref lock conflicts), Go module/build cache, exelet-fs download, shelley UI deps, and VM snapshots.

## Cleanup cron

Runs every 15 minutes. Destroys VMs running >30 min (timestamp parsed from VM name, not disk mtime), removes orphaned disk images, and prunes old snapshot caches.

## Disk layout

```
/dev/md0 (4x NVMe RAID0) → /data (ext4, 12.6TB)
/var/lib/libvirt/images → /data/libvirt/images (symlink)
```

## Deploy key

Read-only SSH deploy key at `/etc/edric-ci-deploy-key`, owned `root:ci-runners 0640`. Used by the warmup script for git prefetch. The key itself is a secret — not in this repo.

## Other docs

See also devdocs/ci.md. Probably we should rationalize these two separate docs. Later.
