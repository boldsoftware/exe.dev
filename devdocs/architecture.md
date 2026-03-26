# Architecture

exe.dev provides VMs over SSH. Three services handle the work:

```
                      ┌─────────────┐
        SSH ─────────►│   sshpiper   │
                      └──────┬──────┘
                             │ gRPC plugin
                             ▼
  HTTPS ──────►  ┌──────────────────────┐   gRPC   ┌──────────────┐
  (*.exe.xyz)    │         exed         │ ────────► │   exelet(s)  │
                 │  (control plane)     │           │  (compute)   │
                 │  SQLite · Stripe     │           │  Cloud HV    │
                 └──────────────────────┘           └──────────────┘
                             ▲
                             │ gRPC
                      ┌──────┴──────┐
                      │  exeprox(s) │  ← regional reverse proxies
                      │   (HTTPS)   │    for *.exe.xyz traffic
                      └─────────────┘
```

**exed** — single control-plane server. Owns the SQLite database,
user accounts, billing (Stripe), and SSH session routing. In local dev, runs sshpiper as a co-process; in production,
sshpiper is a separate systemd service. Either way, sshpiper calls
back into exed over a gRPC plugin interface to decide where to route
each SSH connection. Also
exposes a gRPC service for exeprox clients and HTTP/HTTPS endpoints
for the web UI and API.

**exelet** — compute worker (one per host machine). Manages VM
lifecycle via Cloud Hypervisor, with pluggable storage (ZFS, with
support for tiered storage pools and replication), NAT networking,
a metadata service for integrations, and per-VM SSH proxy ports.
Receives gRPC calls from exed. Supports desired-state sync from
exed and resource management. See `exelet/README.md` for detailed
internals.

**exeprox** — regional reverse proxy. Terminates HTTPS traffic for
`*.exe.xyz` hostnames and forwards it to the appropriate VM via SSH
tunnels to exelets. Also listens on thousands of per-VM proxy ports.
Multiple instances run in different regions; they cache routing data
from exed via gRPC.

## SSH

Users SSH to exe.dev. sshpiper intercepts the connection and asks
exed (gRPC) how to route it. Connections addressed to `host.exe.xyz`
are forwarded to the VM's SSH server. Other connections (e.g. the
exe.dev shell) are handled by exed directly.

## HTTP

HTTPS requests to `*.exe.xyz` are handled by exeprox, which resolves
the target VM via exed's gRPC service (BoxInfo, UserInfo, CookieInfo,
TopLevelCert) and reverse-proxies the request to the VM through an
SSH tunnel pool to the exelet.

## VM Creation

exed picks an exelet, then the exelet:

1. Creates a ZFS storage volume
2. Fetches the OCI container image and a custom kernel
3. Writes SSH keys, hostname, and networking config into the rootfs
4. Creates a TAP device and configures NAT networking
5. Boots a Cloud Hypervisor VM with the volume attached

VMs are isolated via KVM. Networking is bridged NAT.
