---
title: What is Shelley?
description: Our coding agent
subheading: "3. Shelley"
suborder: 1
published: true
---

Shelley is a coding agent. It is web-based, works on mobile, and, when you
start an `exe.dev` VM with the default `exeuntu` image, it is running on port
9999, and you can access it securely at `https://vmname.exe.xyz:9999/`.

You can ask Shelley to install software (e.g., run a Marimo notebook on port
8000), build a web site, browse the web, and anything in between. That said,
you don't have to use Shelley if you don't want to. Other coding agents run
just fine on `exe.dev` VMs and some are pre-installed on our default image.
If you want, disable it with `sudo systemctl disable --now shelley.service`.

Shelley is so named because the main tool it uses is the shell, and I like
putting "-ey" at the end of words. It is also named after Percy Bysshe Shelley,
with an appropriately ironic nod at
"[Ozymandias](https://www.poetryfoundation.org/poems/46565/ozymandias)."
Shelley is a computer program, and, it's an it.
