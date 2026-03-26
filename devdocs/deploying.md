# Deploying

## Commit Queue

All changes ship via `./bin/q`. No PRs, no code reviews. Export `EXE_SLACK_BOT_TOKEN` (from 1Password) before deploying.

## Deploying exed

```
./ops/deploy/deploy-exed-prod.sh
```

Preview what would ship: `./ops/deploy/deploy-what-exed.sh`

Prod machine is `exed-02` (`ssh ubuntu@exed-02` via Tailscale).

## Deploying exelet

```
./ops/deploy/deploy-exelet-prod.sh <machine-name>
./ops/deploy/deploy-exelet-staging.sh <machine-name>
```

New compute hosts: `./ops/deploy/setup-exelet-host.sh`

Rebuilding kernel/rovol images: see [exelet-fs.md](exelet-fs.md).

## Exeuntu Image

Default VM image: Ubuntu 24.04 with dev tools. Hosted at `ghcr.io/boldsoftware/exeuntu:latest`.

Auto-built via GitHub Actions when `exeuntu/` or the build workflow change. (Shelley is installed at VM creation time, not baked into exeuntu.)

For local builds and testing with a local registry, see [exeuntu-local-testing.md](exeuntu-local-testing.md).
