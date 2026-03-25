---
title: API
description: Programmatic access via SSH
subheading: "2. Features"
---

The exe.dev API is SSH. Run commands like `ssh exe.dev ls --json` or `ssh exe.dev new --json`
directly from scripts and automation. See the [CLI Reference](/docs/section/9-cli-reference) for the full list of commands.

For example:

```
$ ssh exe.dev ls --json | jq '.vms[0]'
{
  "https_url": "https://bloggy.exe.xyz",
  "region": "lon",
  "region_display": "London, UK",
  "ssh_dest": "bloggy.exe.xyz",
  "status": "running",
  "vm_name": "bloggy"
}
```
