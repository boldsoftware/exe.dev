---
title: Shelley, our Agent
description: Prototype quickly
subheading: "2. Features"
---

Shelley is a coding agent. It is web-based, works on mobile, and, when you
start an `exe.dev` box with the default `exeuntu` image, it is running on port
9999, and you can access it securely at `https://NAME.exe.xyz:9999/`.

You can ask Shelley to install software (e.g., run a Marimo notebook on port
8000), build a web site, browse the web, and anything in between. That said,
you don't have to use Shelley if you don't want to. Other coding agents run
just fine on `exe.dev` boxes and some are pre-installed on our default image.
If you want, disable it with `sudo systemctl disable --now shelley.service`.

Shelley proxies its LLM API usage through `exe.dev`'s LLM Gateway. Your account
comes with dollars to use for LLM tokens. This lets you get started without
registering for LLM API keys. You are welcome to, of course, configure Shelley
to use your own API keys.

## Upgrading

Since Shelley is running on your box, you're running the version that
existed when you created your box. Run `shelley install <box>` in
the exe.dev shell to update it.
