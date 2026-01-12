---
title: Frequent Asked Questions
description: and some infrequently asked ones too
subheading: "6. Other"
suborder: 1
published: true
---

# Q: What is the host key for exe.dev?

When you first `ssh exe.dev` you are looking for the fingerprint:

```
SHA256:JJOP/lwiBGOMilfONPWZCXUrfK154cnJFXcqlsi6lPo.
```

Ensuring that fingerprint is displayed the first time means that
and all future connections from that device are going directly
to `exe.dev`.

# Q: How do I use a specific SSH key for exe.dev?

If you want to specify which key to use, use `ssh -i ~/.ssh/id_ed25519_exe exe.dev` or add the following stanza to your `~/.ssh/config`:

```
Host exe.dev
  IdentitiesOnly yes
  IdentityFile ~/.ssh/id_ed25519_exe
```

# Q: How do I connect VSCode to my VM?

On your dashboard, at [https://exe.dev/](https://exe.dev/), there are links
to open in VSCode. This leverages VSCode's SSH remote features.
The link is of the form:

```
vscode://vscode-remote/ssh-remote+<vmname>.exe.xyz/home/exedev?windowId=_blank
```

The `/home/exedev` in that URL is the path on the filesystem for VSCode to
consider as your workspace.

# Q: How do I copy files to/from my VM?

Use `scp`. For example, `scp <local-file> <vmname>.exe.xyz:`.

# Q: Can I run docker images?

Sure, why not; it's just a VM. If you start with the `exeuntu` image,
you can run `docker run --rm alpine:latest echo hello`, and go from there!

# Q: How do you pronounce "exe"?

We pronounce it "EX-ee". But you don't have to.

# Q: How do I access GitHub? How do I set up a minimal GitHub token?

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
