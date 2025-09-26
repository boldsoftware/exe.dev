---
title: What is exe?
description: exe provides persistent development containers reachable over SSH and the browser.
subheading: "1. Introduction"
suborder: 1
published: true
---

exe is the service that provisions persistent development containers on exe.dev.

Each user receives a Linux container with durable storage, predictable resources, and first-class SSH access. Containers run on hardened hosts that exe manages for production, CI, and macOS development workflows.

## What problems does exe solve?

Developers need a place where code, tools, and long-running services can stay alive even after the terminal disconnects. Local laptops are fragile; ad-hoc cloud servers are hard to maintain. exe keeps the environment online so you can drop back in instantly.

- **Persistent storage:** disks are preserved across restarts, so git repos and databases stick around.
- **Fast SSH access:** log in with your existing keys and land in the same container every time.
- **Controller-managed hosts:** exe orchestrates container hosts through `ctr-host` automation.
- **Integrated tooling:** the web UI mirrors SSH output while staying minimal for production use.

## How does it work?

The `exed` controller accepts web and SSH connections, authenticates users, and schedules their containers. It communicates with container hosts over secure channels, wiring up networking, disks, and metrics. Hosts are configured via the scripts in `ops/` for production, CI, and macOS lima environments.

When you connect to `ssh exe.dev`, the shell you see is running inside that managed container. The controller keeps logs sparse on purpose -- only the essential prompts appear in the terminal UI.

## How do I get access?

Access is invite-only while we scale the fleet. Join the waitlist from the docs navigation and we will reach out when capacity opens up.

## Where to go next?

Continue through the documentation to learn about deployment modes, container lifecycle management, and integrating exe with your workflow. The sidebar on the left lists everything that is currently available.
