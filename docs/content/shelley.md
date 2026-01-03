---
title: Shelley, our Agent
description: Prototype quickly
subheading: "2. Features"
---

Shelley is a coding agent. It is web-based, works on mobile, and, when you
start an `exe.dev` VM with the default `exeuntu` image, it is running on port
9999, and you can access it securely at `https://vmname.exe.xyz:9999/`.

You can ask Shelley to install software (e.g., run a Marimo notebook on port
8000), build a web site, browse the web, and anything in between. That said,
you don't have to use Shelley if you don't want to. Other coding agents run
just fine on `exe.dev` VMs and some are pre-installed on our default image.
If you want, disable it with `sudo systemctl disable --now shelley.service`.

Shelley proxies its LLM API usage through `exe.dev`'s LLM Gateway. Your account
comes with dollars to use for LLM tokens. This lets you get started without
registering for LLM API keys. You are welcome to, of course, configure Shelley
to use your own API keys.

Shelley is so named because the main tool it uses is the shell, and I like
putting "-ey" at the end of words. It is also named after Percy Bysshe Shelley,
with an appropriately ironic nod at
"[Ozymandias](https://www.poetryfoundation.org/poems/46565/ozymandias)."
Shelley is a computer program, and, it's an it.

## AGENTS.md

Shelley reads guidance files, specifically:
* personal `AGENTS.md` file at `~/.config/shelley/AGENTS.md`
* project `AGENTS.md` files in the git root or working directory

Shelley will also notice `CLAUDE.md` and `DEAR_LLM.md` files.

## Upgrading

Since Shelley is running on your VM, you're running the version that
existed when you created your VM. Run `shelley install <vm>` in
the exe.dev shell to update it.
