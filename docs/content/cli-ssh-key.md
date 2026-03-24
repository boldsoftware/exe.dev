---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "ssh-key"
description: "Manage SSH keys for your account"
subheading: "9. CLI Reference"
suborder: 12
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
ssh-key add 'ssh-ed25519 AAAA... my-laptop'

To generate a new key locally:
  ssh-keygen -t ed25519 -C "mnemonic-for-this-key" -f ~/.ssh/id_exe

The -C flag sets a name for the key.

Then add the public key from your local shell:
  cat ~/.ssh/id_exe.pub | ssh exe.dev ssh-key add

Or from the exe.dev shell:
  ssh-key add 'ssh-ed25519 AAAA... my-laptop'
```

### ssh-key remove

Remove an SSH key from your account

**Usage:**
```
ssh-key remove <name|fingerprint|public-key>
```

**Options:**
- `--json`: output in JSON format

### ssh-key rename

Rename an SSH key

**Usage:**
```
ssh-key rename <old-name> <new-name>
```

**Options:**
- `--json`: output in JSON format

