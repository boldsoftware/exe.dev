---
title: API
description: Programmatic access via SSH
subheading: "2. Features"
---

The exe.dev API is SSH. Run commands like `ssh exe.dev ls --json` or `ssh exe.dev new --json`
directly from scripts and automation.

For example:

```
$ssh exe.dev ls --json | jq '.vms[0]'
{
  "image": "boldsoftware/exeuntu",
  "ssh_dest": "bloggy.exe.xyz",
  "status": "running",
  "vm_name": "bloggy"
}
```
