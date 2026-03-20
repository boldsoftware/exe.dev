---
name: install-node
description: Use when the user needs Node.js or npm installed, or when a task requires Node.js and it is not available.
---

Install Node.js using nodeenv via uvx:

```bash
uvx nodeenv --lts ~/node
mkdir -p ~/.local/bin
ln -sf ~/node/bin/node ~/.local/bin/node
ln -sf ~/node/bin/npm ~/.local/bin/npm
ln -sf ~/node/bin/npx ~/.local/bin/npx
```

This installs the latest LTS version of Node.js into ~/node and symlinks the binaries into ~/.local/bin (which is on PATH).
