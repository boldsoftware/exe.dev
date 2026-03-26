# CI Infrastructure

CI runs on Buildkite. The pipeline is defined in `.buildkite/` and runs on a self-hosted machine.

## Machine: exe-ci-01

Ubuntu 24.04, 48 vCPUs (AMD EPYC 9254), 377 GiB RAM, 3× 3.5 TB NVMe in ZFS pool.

See [.buildkite/MACHINE_SETUP.md](../.buildkite/MACHINE_SETUP.md) for full setup instructions.

The Buildkite agent runs as `buildkite-agent` with `spawn=16` (16 concurrent agent processes). VM disk images live on ZFS (`/data/libvirt/images`).

## Pipeline

`.buildkite/steps/generate-pipeline.py` dynamically assembles the pipeline from YAML segments based on which files changed:

| Segment | When |
|---------|------|
| `commit-validation.yml` | Always |
| `exe.yml` | exe files changed |
| `shelley.yml` | shelley/ changed |
| `blog.yml` | blog/ or cmd/blogd/ changed |
| `format.yml` | Always |
| `push.yml` | `kite-queue-*` branches only |

E1e test shards are generated dynamically. The pipeline balances them by execution time using weights in `generate-pipeline.py`.

## Tuning

Override via commit trailers or environment variables:

| Parameter | Default | Trailer |
|-----------|---------|---------|
| E1E_SHARDS | 5 | `E1E-Shards: N` |
| E1E_VM_CONCURRENCY | 12 | `E1E-VM-Concurrency: N` |
| E1E_GOMAXPROCS | (Go default) | `E1E-GOMAXPROCS: N` |
| E1E_EXELETS_VM_CONCURRENCY | 10 | `E1E-Exelets-VM-Concurrency: N` |

Coverage mode: commit trailer `Coverage: true` or env `E1E_COVERAGE=true`.

## e1e Test Isolation

Each e1e shard creates Cloud Hypervisor VMs for testing (via `ops/ci-vm.py`). VMs are isolated by a unique prefix derived from the shard number, so cleanup only destroys that shard's VMs.

## VM Snapshot Cache

Snapshots are keyed by `date +%Y%m%d` + ops/ tree hash + exeuntu image digest. They regenerate daily. The first run of the day takes longer (VM boot + setup); subsequent runs reuse the snapshot.

## Shelley Tests

Shelley tests run Playwright E2E tests against the shelley UI. Node.js, pnpm, and Chromium dependencies are installed on the CI machine.

## Debugging

```bash
# Check agent status
ssh exe-ci-01 sudo systemctl status buildkite-agent

# Check running VMs
ssh exe-ci-01 'sudo virsh list --all'

# Agent logs
ssh exe-ci-01 sudo journalctl -u buildkite-agent --since "1 hour ago" --no-pager
```

Build logs and artifacts are in the Buildkite UI.

## Edric (Legacy)

Edric was the previous CI machine running 16 GitHub Actions runners. It is at end-of-life. Configuration files in `ops/ci/` and GitHub Actions workflows in `.github/workflows/` are from this era. See `ops/ci/readme_edric.md` for historical reference.
