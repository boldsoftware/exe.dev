---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "resize"
description: "Resize a VM's disk"
subheading: "9. CLI Reference"
suborder: 10
published: true
---

# resize

Resize a VM's disk

## Usage

```
resize <vmname> --disk=<size>
```

## Options

- `--disk`: new total disk size (e.g., 25, 25GB) - must be larger than current size
- `--json`: output in JSON format

