---
title: The GUTS Stack
description: Go, Unix, TypeScript, SQLite
subheading: "7. Editorials"
---

If you use our default `exeuntu` image and our Shelley coding agent, you'll
start with a GUTS template: the "welcome" server we wrote is implemented in Go
and uses SQLite as its database. (At time of writing, we haven't built out much UI,
so the TypeScript is rather minimal; coming soon.) If you don't
specify an alternative, Shelley will build on that architecture, and we think
you'll have good, performant results.

Whether your VM is running a sandbox or prod, we believe this simpler
stack makes sense. Websites are inherently distributed systems (the client is a browser),
but a single, simple back end can scale for a long time. Modern
machines are fast and disks are big. (exe.dev disks are persisted and backed up.)

Kubernetes, serverless functions, distributed transactions, edge computing, and
so on all have their place, but we place our bets on the humble monolith.
