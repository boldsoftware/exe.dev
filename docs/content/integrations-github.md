---
title: GitHub Integration
description: Connect your GitHub account to exe.dev for private repo access
subheading: "3. Integrations"
suborder: 3
published: true
---

Instead of [setting up a GitHub personal access token](faq/github-token),
the GitHub integration connects your GitHub account to exe.dev so that you
can work on private repos without managing tokens, and without having
tokens on the VM itself.

## Linking your GitHub account

Link your GitHub account from the [Integrations page](/integrations).

The exe.dev GitHub App will need to be installed into your account or into
your organization. If someone else has already installed it, you may need
to sign into your account instead of clicking the install button.

## Creating repo integrations

Once connected, create per-repo integrations:

```
exe.dev ▶ integrations add github --name blog --repository ghuser/blog --attach vm:my-vm
Added integration blog

Usage from a VM:
  ssh my-vm 'cd $(mktemp -d) && git clone https://blog.int.exe.xyz/ghuser/blog.git'
```

Then, from inside the VM:

```
git clone https://blog.int.exe.xyz/ghuser/blog.git
```

## Using the `gh` CLI

The integration also supports the GitHub CLI (`gh`). Set `GH_HOST` to the
integration hostname:

```
export GH_HOST=blog.int.exe.xyz
gh repo view ghuser/blog
gh issue list -R ghuser/blog
gh pr list -R ghuser/blog
```
