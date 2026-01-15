---
title: How does exe.dev work?
description: behind-the-scenes look
subheading: "4. FAQ"
suborder: 10
published: true
---

You're an engineer. We're engineers. Let's talk about what's going on under the
hood.

An "exe.dev" VM runs on a bare metal machine that exe.dev rents. We happen to
use Cloud Hypervisor, but that's a bit of an implementation detail (and may
change!).

With most providers, your VM starts with a "base image" and is given a block
device. Exe.dev instead starts with a container image (by default, "exeuntu"),
and hooks it up with a block device with the image on it. This makes creating a
new VM take about two seconds. In exchange, we lose some flexibility: you don't
get to choose which kernel you're using.

On the networking side, we don't give your VM its own public IP.
Instead, we terminate HTTPS/TLS requests, and proxy them securely
to your VM's web servers. For SSH, we handle `ssh vmname.exe.xyz`.
