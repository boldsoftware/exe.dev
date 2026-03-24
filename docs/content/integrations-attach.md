---
title: Attaching Integrations
description: Attach integrations to VMs using direct attachment, tags, or auto-attach
subheading: "3. Integrations"
suborder: 4
published: true
---

Integrations can be attached to specific VMs, to all VMs, or to tags.

## Attach to a specific VM

```
exe.dev ▶ integrations attach blog vm:my-vm
```

## Attach to all VMs

```
exe.dev ▶ integrations attach blog auto:all
```

## Attach to a tag

```
exe.dev ▶ integrations attach blog tag:prod
```

Any VM with the `prod` tag will have the `blog` integration available.
You can tag VMs with:

```
exe.dev ▶ tag my-vm prod
```

## Attach at creation time

You can attach integrations when creating them with `--attach`:

```
exe.dev ▶ integrations add github --name blog --repository ghuser/blog --attach auto:all
exe.dev ▶ integrations add http-proxy --name myapi --target https://api.example.com --bearer sk-... --attach tag:prod
```

You can also attach integrations when creating a new VM with `--integration`:

```
exe.dev ▶ new --name my-vm --integration blog --integration myapi
```

## Detach

```
exe.dev ▶ integrations detach blog vm:my-vm
```
