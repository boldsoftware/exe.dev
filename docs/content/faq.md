---
title: Frequent Asked Questions
description: and some infrequently asked ones too
subheading: "5. Other"
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

# Q: Why is it `ssh name@exe.dev` and not `ssh name.exe.dev`?

We can make `https://name.exe.dev/` work because HTTP has a "Host:" header that
lets us direct traffic appropriately. The SSH protocol only has the IP address
that's being connected to.

# Q: Can I run docker images?

Sure, why not; it's just a VM. If you start with the `exeuntu` image,
you can run `docker run --rm alpine:latest echo hello`, and go from there!

# Q: How do you pronounce "exe"?

We pronounce it "EX-ee". But you don't have to.
