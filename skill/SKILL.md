---
name: using-exe-dev
description: Guides working with exe.dev VMs. Use when the user mentions exe.dev, exe VMs, *.exe.xyz, or tasks involving exe.dev infrastructure.
---

# About

exe.dev provides Linux VMs with persistent disks, instant HTTPS, and built-in auth. All management is via SSH.

## Documentation

- Docs index: https://exe.dev/docs.md
- All docs in one page (big!): https://exe.dev/docs/all.md

The index is organized for progressive discovery: start there and follow links as needed.

## Quick reference

```
ssh exe.dev help             # show commands
ssh exe.dev help <command>   # show command details
ssh exe.dev new --json       # create VM
ssh exe.dev ls --json        # list VMs
ssh exe.dev rm <vm>          # delete VM
ssh <vm>.exe.xyz             # connect to VM
scp file.txt <vm>.exe.xyz:~/ # transfer file
```

Every VM gets `https://<vm>.exe.xyz/` with automatic TLS.

## Naming

VM names are globally unique across all exe.dev customers. If `new --name=<n>` errors with `"this VM name is not available"`, the slug is taken (by you or someone else) — pick another. Generic names like `app`, `staging`, `demo` are mostly gone; add a suffix (`<purpose>-<yyyymmdd>` or a short random) for any new VM you intend to keep.

## A tale of two SSH destinations

- **`ssh exe.dev <command>`** — the exe.dev lobby. A REPL for VM lifecycle, sharing, and configuration. Does not support scp, sftp, or arbitrary shell commands.
- **`ssh <vm>.exe.xyz`** — a direct connection to a VM. Full SSH: shell, scp, sftp, port forwarding, everything.

## Working in non-interactive and sandboxed environments

Coding agents often run SSH in non-interactive shells or sandboxes. Common issues and workarounds:

**scp/sftp failures**: Ensure you're targeting `<vm>.exe.xyz` rather than `exe.dev`. Use ssh-based workarounds.

**Hung connections**: Non-interactive SSH can block on host key prompts with no visible output. Use `-o StrictHostKeyChecking=accept-new` on first connection to a new VM.

**SSH config**: Check whether both destinations are configured to use the right key:

```
Host exe.dev *.exe.xyz
  IdentitiesOnly yes
  IdentityFile ~/.ssh/id_ed25519
```

**Multi-line commands**: Heredocs and embedded newlines collapse when passed inside a quoted argument to `ssh <vm>.exe.xyz "<cmd>"` from many shells (`\n` flattens to spaces, breaking scripts). Feed multi-line scripts via `bash -s` over stdin instead:

```
ssh <vm>.exe.xyz 'bash -s' <<'EOF'
set -e
sudo apt-get update
# ...
EOF
```

For non-text content (binaries, anything sensitive to whitespace), base64-encode locally and decode on the VM.

**Lobby-routed VM shell**: `ssh exe.dev ssh <vm> "<cmd>"` runs a shell command on the VM via the lobby. Convenient in non-interactive scripts — the lobby's host key is already trusted, so there's no per-VM `known_hosts` setup or `accept-new` prompt. (No scp/sftp through this path; use direct `<vm>.exe.xyz` for transfers.)
