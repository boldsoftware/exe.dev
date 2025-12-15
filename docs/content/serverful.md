---
title: Persistent disks, not serverless
description: exe.dev is serverful, not serverless
subheading: "5. Editorials"
---

Most serverless Platform-as-a-Service offerings don't give you a persistent disk.
This is a productivity killer.

At exe.dev, your VM comes with a normal, boring file system. Run a database
on it. Write logs to it. Use sqlite. Store files.

Immutable infrastructure has its place, but it's not the only way to go.
