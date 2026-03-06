---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "tag"
description: "Add or remove a tag on a VM"
subheading: "8. CLI Reference"
suborder: 8
---

# tag

Add or remove a tag on a VM

## Usage

```
tag [-d] <vm> <tag-name>
```

## Options

- `--d`: delete tag
- `--json`: output in JSON format

## Examples

```
tag my-vm prod        # add tag
tag -d my-vm prod     # remove tag
```

