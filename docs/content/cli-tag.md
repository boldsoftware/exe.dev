---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "tag"
description: "Add or remove tags on a VM"
subheading: "9. CLI Reference"
suborder: 8
published: true
---

# tag

Add or remove tags on a VM

## Usage

```
tag [-d] <vm> <tag-name> [tag-name...]
```

## Options

- `--d`: delete tag
- `--json`: output in JSON format

## Examples

```
tag my-vm prod web        # add tags
tag -d my-vm prod web     # remove tags
```

