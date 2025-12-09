---
title: Spinning up a Marimo Notebook
description: an open source reactive notebook
subheading: "3. Use Cases"
published: true
---

*tl;dr:* `ssh exe.dev new --image=ghcr.io/marimo-team/marimo:latest-sql`

<img width="100%" src="https://boldsoftware.github.io/public_html/marimo.png">

[Marimo](https://marimo.io/) is a reactive Python notebook. To run it on exe.dev, register
for exe.dev with `ssh exe.dev`, and then run
`ssh exe.dev new --image=ghcr.io/marimo-team/marimo:latest-sql` in your terminal.
It'll look like so:

```
$ ssh exe.dev new --image=ghcr.io/marimo-team/marimo:latest-sql
Creating nan-tango using image marimo-team/marimo:latest-sql...

App (HTTPS proxy → :8080)
https://nan-tango.exe.xyz

SSH
ssh nan-tango@exe.dev
```

Finally, follow the `https://<box-name>.exe.xyz/` provided. You're all set.
When you're done, `ssh exe.dev rm <box-name>` to clean up.
