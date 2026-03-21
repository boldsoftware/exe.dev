---
title: What are Integrations?
description: Integrate exe.dev with other tools and services
subheading: "9. Integrations"
suborder: 1
preview: true
---

Integrations connect your exe.dev VM to other services securely and flexibly.
They allow you to "inject secrets" on the network, so that those secrets cannot
be extracted from the VM itself. Integrations are created with the `integrations add`
command and attached to VMs with the `integrations attach` command.

You can manage integrations from the [Integrations page](/integrations) in the
web UI or via SSH.

There are two types of integrations:

- [HTTP Proxy Integration](integrations-http-proxy) — inject headers into HTTP requests
- [GitHub Integration](integrations-github) — work with private repos without managing tokens

See also: [Attaching Integrations](integrations-attach) for how to connect
integrations to your VMs using direct attachment, tags, or auto-attach.
