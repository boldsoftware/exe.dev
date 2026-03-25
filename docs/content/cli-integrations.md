---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "integrations"
description: "Manage integrations"
subheading: "9. CLI Reference"
suborder: 13
published: true
---

# integrations

Manage integrations

## Usage

```
integrations <subcommand> [args...]
```

## Aliases

int

## Subcommands

### integrations list

List your integrations

**Usage:**
```
integrations list
```

**Options:**
- `--json`: output in JSON format

### integrations setup

Set up a service integration

**Usage:**
```
integrations setup <type> [-d]
```

**Options:**
- `--d`: disconnect GitHub account
- `--delete`: disconnect GitHub account
- `--list`: list connected GitHub accounts
- `--verify`: verify GitHub connections are working

### integrations add

Add a new integration

**Usage:**
```
integrations add <type> --name=<name> [args...]
```

**Options:**
- `--attach`: attach to a spec (vm:<name>, tag:<name>, or auto:all); can be repeated
- `--bearer`: bearer token (shorthand for --header="Authorization:Bearer TOKEN")
- `--header`: header to inject (e.g. X-Auth:secret)
- `--name`: integration name (required)
- `--repository`: GitHub repository in owner/repo format (required for github)
- `--target`: target URL (required for http-proxy)

### integrations remove

Remove an integration

**Usage:**
```
integrations remove <name>
```

### integrations attach

Attach an integration to a VM, tag, or all VMs

A <spec> controls where the integration is mounted:
  vm:<vm-name>   attach to a specific VM
  tag:<tag-name> attach to every VM with the given tag
  auto:all       attach to all current and future VMs

You can attach the same integration multiple times with different specs.

**Usage:**
```
integrations attach <name> <spec>
```

**Examples:**
```
int attach my-mcp vm:dev1
int attach my-mcp tag:production
int attach my-mcp auto:all
```

### integrations detach

Detach an integration from a VM, tag, or all VMs

**Usage:**
```
integrations detach <name> <spec>
```

### integrations rename

Rename an integration

**Usage:**
```
integrations rename <name> <new-name>
```

