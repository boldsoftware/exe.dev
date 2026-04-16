---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "cp"
description: "Copy an existing VM"
subheading: "9. CLI Reference"
suborder: 10
published: true
---

# cp

Copy an existing VM

## Usage

```
cp <source-vm> [new-name] [--copy-tags=false]
```

## Options

- `--copy-tags`: copy tags from source VM (use --copy-tags=false to disable)
- `--disk`: disk size (e.g., 20, 20GB, 50G)
- `--json`: output in JSON format

## Examples

```
cp my-vm              # copy with auto-generated name
cp my-vm my-vm-copy   # copy with specific name
cp my-vm --copy-tags=false  # copy without tags
```

