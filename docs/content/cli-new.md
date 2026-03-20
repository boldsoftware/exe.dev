---
# DO NOT EDIT; rebuild with go run ./cmd/gencmddocs
title: "new"
description: "Create a new VM"
subheading: "8. CLI Reference"
suborder: 4
---

# new

Create a new VM

## Options

- `--command`: container command: auto, none, or a custom command
- `--env`: environment variable in KEY=VALUE format (can be specified multiple times)
- `--image`: container image
- `--integration`: integration name to attach (can be specified multiple times or comma-separated)
- `--json`: output in JSON format
- `--name`: VM name (auto-generated if not specified)
- `--no-email`: do not send email notification
- `--prompt`: initial prompt to send to Shelley after VM creation (requires exeuntu image); use /dev/stdin to read from stdin

## Examples

```
new                                     # just give me a computer
new --name=b --image=ubuntu:22.04       # custom image and name
new --env FOO=bar --env BAZ=qux         # with environment variables
new --integration=myproxy               # attach an integration
echo 'build me a web app' | ssh exe.dev new --prompt=/dev/stdin
```

