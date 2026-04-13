# Local Development

## Prerequisites

Cloud Hypervisor requires Linux and KVM.

### macOS

Lima VMs provide the Linux/KVM environment. Apple M3+ required for nested virtualization.

```
brew install tailscale coreutils lima node uv zstd
tailscale up
```

### Linux

KVM is available natively — no Lima needed. Install Go, Node, uv, zstd, and Tailscale.

On an exe.dev VM, everything is pre-installed. Build exelet first to avoid OOM from concurrent Go compilations on small VMs:

```
make exelet
go build -o /tmp/exed-local ./cmd/exed/
/tmp/exed-local -stage=local -start-exelet -start-metricsd -db tmp
```

The rest of this doc covers the macOS (Lima) workflow. On Linux, `make run-devlet` starts exelet locally instead of on a Lima VM — everything else is the same.

## Lima VM Setup (macOS)

Create two Lima VMs — `exe-ctr` for manual dev, `exe-ctr-tests` for Go tests:

```
./ops/setup-lima-hosts.sh all
```

This builds a base VM image (slow the first time — builds Cloud Hypervisor from source), then clones it into both instances. The script also configures `~/.ssh/config` for `lima-exe-ctr.local` and `lima-exe-ctr-tests.local`.

Reset VMs to a clean state:

```
./ops/setup-lima-hosts.sh reset              # both
./ops/setup-lima-hosts.sh reset-exe-ctr      # just dev VM
./ops/setup-lima-hosts.sh reset-exe-ctr-tests # just test VM
```

## Running the Stack

Run exed and exelet together:

```
make run-devlet
```

This does `LOG_LEVEL=debug go run ./cmd/exed -stage=local -http=:8080 -ssh=:2223 -start-exelet -start-metricsd`. On macOS, exed builds the exelet binary (`make exelet`, which includes kernel/rovol image download), copies it to `lima-exe-ctr`, and starts it over SSH. The first build is slow — kernel images are cached after that.

Once running:
- `ssh -p 2222 localhost` or `ssh -p 2222 <machine>@localhost`
- http://localhost:8080
- `scp -P 2222 junk.txt localhost:junk.txt`

### Multi-exelet mode

Run exelets on both Lima VMs:

```
make run-devlets
```

This adds `-multi-exelet`, which starts exelets on both `lima-exe-ctr` and `lima-exe-ctr-tests`. May interact badly with concurrent automated tests.

### whoami DB

`make whoami` downloads the GitHub user lookup database from Backblaze (requires `uv` and `zstd`). The `-gh-whoami` flag defaults to `ghuser/whoami.sqlite3`, so this works from the repo root with no extra flags.

### GITHUB_TOKEN

Set `GITHUB_TOKEN` to a fine-grained personal access token with **no permissions** (public repos only): https://github.com/settings/personal-access-tokens

Used by `ghuser` for GitHub API lookups.

## Running exed and exelet Separately

For faster iteration on a single component.

### exelet

Build (`make exelet`), copy to the Lima VM, and run directly:

```
scp exeletd lima-exe-ctr.local:
limactl shell exe-ctr -- sudo ./exeletd \
  -D \
  --stage local \
  --data-dir /data/exelet \
  --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" \
  --network-manager-address nat:///data/exelet/network \
  --runtime-address cloudhypervisor:///data/exelet/runtime \
  --listen-address tcp://:9080 \
  --exed-url "http://$(ssh lima-exe-ctr.local getent ahostsv4 _gateway | grep _gateway | awk '{ print $1 }')"
```

Debug endpoints (pprof, version, metrics) at `http://localhost:9081/debug` (change with `--http-addr`).

### exed

After starting exelet and downloading the whoami database (`make whoami`):

```
go run ./cmd/exed -stage=local -exelet-addresses tcp://127.0.0.1:9080
```

## CTR_HOST

Go tests use `CTR_HOST` to find a container host. Defaults to `lima-exe-ctr-tests` when running under `go test`, `lima-exe-ctr` otherwise. In CI, it points to a dedicated VM. The `-start-exelet` flag ignores `CTR_HOST` and always uses `lima-exe-ctr`.
