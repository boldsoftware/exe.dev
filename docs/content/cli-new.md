---
# GENERATED; rebuild with go run ./cmd/gencmddocs
title: "new"
description: "Create a new box"
subheading: "4. CLI Reference"
suborder: 4
---

# new

Create a new box

## Options

- `--command`: container command: auto, none, or a custom command
- `--env`: environment variable in KEY=VALUE format (can be specified multiple times)
- `--image`: container image
- `--json`: output in JSON format
- `--name`: box name (auto-generated if not specified)
- `--no-email`: do not send email notification
- `--prompt`: initial prompt to send to Shelley after box creation (requires exeuntu image)

## Examples

```
new                                     # just give me a computer
new --name=b --image=ubuntu:22.04       # custom image and name
new --env FOO=bar --env BAZ=qux         # with environment variables
```

