---
title: Frequent Asked Questions
subheading: "2. Other"
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

