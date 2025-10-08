---
title: Frequent Asked Questions
description: and some infrequently asked ones too
subheading: "3. Other"
suborder: 1
published: true
---

# Q: How do I use a specific SSH key for exe.dev?

If you want to specify which key to use, use `ssh -i ~/.ssh/id_ed25519_exe exe.dev` or add the following stanza to your `~/.ssh/config`:

```
Host exe.dev
  IdentitiesOnly yes
  IdentityFile ~/.ssh/id_ed25519_exe
```

# Q: How do I connect VSCode to my box?

On your dashboard, at [https://exe.dev/~](https://exe.dev/~), there are links
to open in VSCode. This leverages VSCode's SSH remote features.
The link is of the form:
```
vscode://vscode-remote/ssh-remote+<box-name>@exe.dev/app?windowId=_blank
```
The `app` in that URL is the path on the filesystem for VSCode to
consider as your workspace.
