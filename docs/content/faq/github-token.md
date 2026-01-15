---
title: How do I access GitHub? How do I set up a minimal GitHub token?
description: GitHub access and fine-grained tokens
subheading: "4. FAQ"
suborder: 7
published: true
---

You can use the `gh` tool to login to GitHub on your VM, and it will
work fine.

If you want to give the VM only access to one repo, and perhaps make
that access read-only, you can use [create a fine-grained personal access token](https://github.com/settings/personal-access-tokens/new).
Choose a single repository, and add the "Contents" permission. Choose read-only or
read-write as your use case desires.

<img width="100%" src="https://boldsoftware.github.io/public_html/ghpat.png">

After doing so, use the token like so:

```
$ cat > token
(paste the token and hit ctrl-d)
$ gh auth login --with-token < token
$ gh auth setup-git
$ git clone https://github.com/USER/REPO
```
