---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "stat"
description: "Show CPU, memory, disk, and IO metrics for a VM"
subheading: "9. CLI Reference"
suborder: 9
published: true
---

# stat

Show CPU, memory, disk, and IO metrics for a VM

## Usage

```
stat <vm-name> [--range=24h|7d|30d]
```

## Options

- `--json`: output in JSON format
- `--range`: time range: 24h, 7d, or 30d

## Examples

```
stat my-vm             # last 24 hours (default)
stat my-vm --range=7d  # last 7 days
stat my-vm --range=30d # last 30 days
```

