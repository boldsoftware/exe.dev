---
title: How do I use a specific SSH key for exe.dev?
description: Configure SSH to use a specific key
subheading: "5. FAQ"
suborder: 2
published: true
---

If you want to specify which key to use, use `ssh -i ~/.ssh/id_ed25519_exe exe.dev` or add the following stanza to your `~/.ssh/config`:

```
Host exe.dev *.exe.xyz
  IdentitiesOnly yes
  IdentityFile ~/.ssh/id_ed25519_exe
```
