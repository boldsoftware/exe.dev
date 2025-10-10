---
title: The GUTS Stack
description: Go, Unix, Typescript, Sqlite
subheading: "3. Editorials"
---

If you use our defaul `exeuntu` image and our Shelley coding agent, you'll
start with a GUTS template: the "welcome" server we wrote is implemented in Go
and uses Sqlite as its database. (At time of writing, we haven't built out a UI
of note, so the TypeScript is somewhat elided; coming soon.) If you don't
specify an alternative, Shelley will build on that architecture, and we think
you'll have good, performant results.

Whether your box is a sandbox or a production box, we believe this simpler
stack makes sense. Web sites are distributed systems (the client is a browser),
but avoiding distributing the back end can scale for a long time. Modern
machines and disks are big.

Kubernetes, serverless functions, distributed transactions, edge computing, and
so on all have their place, but we place our bets on the humble monolith.
