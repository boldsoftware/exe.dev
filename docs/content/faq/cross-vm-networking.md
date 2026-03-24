---
title: How do I connect from one VM to another?
description: SSH, Tailscale, and other tricks
subheading: "5. FAQ"
suborder: 8
published: true
---

VMs, even within one account, are isolated from each other. There
is not a "private network" which connects them.

Some of our users use [Tailscale](https://tailscale.com/) to
create a virtual private network to connect their VMs. Tailscale
allows for exposing SSH (see [the docs](https://tailscale.com/docs/features/tailscale-ssh),
it requires `tailscale set --ssh`), which operates independently
of the SSH access provided by [exe.dev](https://exe.dev/).

You can also connect via SSH, either by forwarding your SSH agent
connection or creating additional keys and registering them
to your account.
