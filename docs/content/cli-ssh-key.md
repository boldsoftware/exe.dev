---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "ssh-key"
description: "Manage SSH keys for your account"
subheading: "8. CLI Reference"
suborder: 9
---

# ssh-key

Manage SSH keys for your account

## Usage

```
ssh-key <subcommand> [args...]
```

## Options

- `--json`: output in JSON format

## Subcommands

### ssh-key list

List all SSH keys associated with your account

**Usage:**
```
ssh-key list
```

**Options:**
- `--json`: output in JSON format

### ssh-key add

Add a new SSH key to your account

**Usage:**
```
ssh-key add <public-key>
```

**Options:**
- `--json`: output in JSON format

**Examples:**
```
ssh-key add 'ssh-ed25519 AAAA... user@host'

To generate a new key locally:
  ssh-keygen -t ed25519 -f ~/.ssh/id_exe

Then add the public key from your local shell:
  ssh exe.dev ssh-key add "\"$(cat ~/.ssh/id_exe.pub)\""

Or from the exe.dev shell:
  ssh-key add 'ssh-ed25519 AAAA... user@host'
```

### ssh-key remove

Remove an SSH key from your account

**Usage:**
```
ssh-key remove <public-key>
```

**Options:**
- `--json`: output in JSON format

