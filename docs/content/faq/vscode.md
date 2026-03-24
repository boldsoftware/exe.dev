---
title: How do I connect VSCode to my VM?
description: Open your VM in VSCode
subheading: "5. FAQ"
suborder: 3
published: true
---

On your dashboard, at [https://exe.dev/](https://exe.dev/), there are links
to open in VSCode. This leverages VSCode's SSH remote features.
The link is of the form:

```
vscode://vscode-remote/ssh-remote+<vmname>.exe.xyz/home/exedev?windowId=_blank
```

The `/home/exedev` in that URL is the path on the filesystem for VSCode to
consider as your workspace.
