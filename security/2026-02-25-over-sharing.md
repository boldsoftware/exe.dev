---
title: Shared VMs exposed all HTTP ports to share recipients
description: When sharing a VM, all HTTP ports were proxied to e-mail-identified share recipients instead of only the specified port.
author: exe.dev team
date: 2026-02-25
severity: medium
published: true
---

Through February 21, 2026, when exe.dev users shared their VM using the "share"
command, ports 3000-9999 were proxied, not just the specifically indicated
port. Users who received a share link could access Shelley, which may not have
been intended. Only users who were logged into exe.dev with their e-mail
address, and with whom the VM was shared either via share link or explicitly
via e-mail address, could access these ports.

We have fixed the issue, and we have notified affected users. There is no
evidence of malicious use.
