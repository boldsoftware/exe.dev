---
title: What are Integrations?
description: Integrate exe.dev with other tools and services
subheading: "3. Integrations"
suborder: 1
published: true
---

Integrations connect your exe.dev VM to other services securely and flexibly.
They allow you to "inject secrets" on the network, so that those secrets cannot
be extracted from the VM itself. Integrations are created with the `integrations add`
command and attached to VMs with the `integrations attach` command.

You can manage integrations from the [Integrations page](/integrations) in the
web UI or via SSH.

There are currently three types of integrations:

- [HTTP Proxy Integration](integrations-http-proxy) — inject headers into HTTP requests
- [GitHub Integration](integrations-github) — work with private repos without managing tokens
- Reflection — exposes VM metadata such as email, tags, comments, and attached integrations

## Default integrations

New accounts get a default Reflection integration named `reflection`. It exposes all reflection fields and is attached with `auto:all`, so every VM can read its metadata from `reflection.int.exe.cloud`. Reinstall it from the Reflection tile on the Integrations page or with:

```
exe.dev ▶ integrations add reflection --name reflection --fields all --attach auto:all
```

See also: [Attaching Integrations](integrations-attach) for how to connect
integrations to your VMs using direct attachment, tags, or auto-attach.
