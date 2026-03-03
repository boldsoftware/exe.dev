---
title: Why exe.dev VMs are persistent
description: On the design decision to make VMs persistent, with persistent disks.
author: Philip Zeyliger
date: 2026-03-03
published: true
embargo: "2026-03-03T08:00:00-08:00"
---

When we were designing [exe.dev](https://exe.dev), we settled on VMs being
persistent, with persistent disks. VMs are *not* “quiesced” when there’s no
network traffic or SSH connections. Disks aren’t wiped clean on reboot.

This flies in the face of modern “stateless,” “immutable,” or “serverless”
infrastructure. Surely, we’re nuts. Why did we do this to our users and to
ourselves?

We want the environment to be familiar; more like a laptop than a remote
container-what’s-it that you have to jump through hoops to even get a shell on.
(Who amongst us hasn’t run `tmate` as part of their CI workflow to pop a shell
in the darn ephemeral machine to figure out what’s going on…)

We don’t want to force you into a distributed system, which is what you have
the moment you have a remote SQL database to store your data. (As a wise person
once told me, “You have a problem. You add a distributed system. Now you have
two problems.”) (And those problems can’t even reliably talk to each other.)

We want cron jobs to just work. Systemd timers too.

Should you want to use a coding agent, we want it to be able to both write AND
operate whatever you’re building. (See also [Software as a Wiki](/software-as-wiki).)

We want to spare you the need to fuss with container registries. We want to
make the easy things obvious.

We don’t want to force you into a heavy-handed tool ecosystem. Heck, we don’t
even want to force you into git when all you want is an internal tool or a
prototype.

We don’t want you to have to plug in API keys for another service, and then
another, and then another to do basic things like receive e-mail or host a web
page or store a file or write to a database.

Every time we talked about quiescing, we realized it would break cron jobs.
Every time we talked about GitHub integration, we groaned about git and
GitHub’s complexity.

So, we settled on VMs that keep running, and we’re doing the interesting work
of scaling our infrastructure to keep those VMs running happily and
effectively. We want our VMs big enough so that you can work on the big
projects you already work on. (Please reach out if they’re not big enough!)

And it’s working. As they say in the industry, we’re drinking our own
champagne. We develop exe using exe VMs with the Shelley coding agent. We serve
this blog on, you guessed it, an exe VM. Our silly link shortener uses exe VMs.
Our log search/analysis database and agent: also an [exe.dev](https://exe.dev)
VM.

Go get yourself an exe.dev VM. Get a few. Get twenty.
