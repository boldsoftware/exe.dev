---
title: How exe.dev works
description: behind-the-scenes look
subheading: "5. Other"
---

You're an engineer. We're engineers. Let's talk about what's going on under the
hood.

An "exe.dev" box runs as a VM on a bare metal machine that exe.dev rents. We
happen to use Kata Containers and Cloud Hypervisor, but that's a bit of an
implementation detail (and may change!).

With most providers, your VM starts with a "base image" and is given a block
device. Exe.dev instead starts with a container image (by default, "exeuntu"),
and hooks up an overlay filesystem to the VM. This makes creating a new box
take about two seconds. In exchange, we lose some flexibility: you don't get to
choose which filesystem you're using, nor which kernel you're using.

On the networking side, we don't give your box its own public IP.
Instead, we terminate HTTPS/TLS requests, and proxy them securely
to your box's web servers. For SSH, we handle `ssh box-name@exe.dev`.
