---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "resize"
description: "Resize a VM's resources (memory, CPU, disk)"
subheading: "9. CLI Reference"
suborder: 10
published: true
---

# resize

Resize a VM's resources (memory, CPU, disk)

## Usage

```
resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>]
```

## Options

- `--cpu`: number of CPUs
- `--disk`: new total disk size (e.g., 25, 25GB) - must be larger than current size
- `--json`: output in JSON format
- `--memory`: memory allocation (e.g., 4, 4GB, 8G)

