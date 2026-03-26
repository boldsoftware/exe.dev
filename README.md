# exe.dev

Fully featured cloud VMs.

See [architecture.md](devdocs/architecture.md) for system design.

## Local Dev Ports

| Process | Host | Ports |
|---------|------|-------|
| **sshpiper** | localhost | 2222 (ssh proxy) |
| **exed** | localhost | 2223 (direct ssh), 2224 (piper plugin grpc), 8080 (http) |
| **exelet** | lima-exe-ctr | 9080 (grpc), 9081 (http debug/metrics) |

## Getting Started

### macOS

```
brew install tailscale coreutils lima node uv zstd
tailscale up
./ops/setup-lima-hosts.sh all
```

M3+ required for nested virtualization. Lima VMs provide the Linux/KVM environment that Cloud Hypervisor needs.

### Linux

Cloud Hypervisor and KVM are available natively — no Lima needed. Install Go, Node, uv, zstd, and Tailscale.

On an exe.dev VM, everything is pre-installed.

See [running-locally.md](devdocs/running-locally.md) for full setup and advanced configuration.

## Development

Run exed and exelet together (first build is slow -- kernel build is cached after that):

```
make run-devlet
```

Once running:
- `ssh -p 2222 localhost` or `ssh -p 2222 <machine>@localhost`
- http://localhost:8080
- `scp -P 2222 junk.txt localhost:junk.txt`

See [local-tls.md](devdocs/local-tls.md) for HTTPS with `*.exe.cloud` subdomains and custom CNAME testing.

## Shipping

No PRs, no code reviews. All changes go through the commit queue via `./bin/q`.

See [deploying.md](devdocs/deploying.md) for deployment commands and [ci.md](devdocs/ci.md) for CI infrastructure.

## Documentation

See [`devdocs/`](devdocs/) for detailed documentation, starting with [architecture.md](devdocs/architecture.md).
