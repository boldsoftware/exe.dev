---
title: ssh exe.dev sometimes asks me to register
description: how to solve heisen-connection issues
subheading: "4. FAQ"
suborder: 11
published: true
---

When you ssh to a server, you authenticate using a public key.
If you have multiple public keys, they are offered to the server one at a time.
Out of privacy concerns, the server must accept or reject each key in turn; it cannot wait to see the full set.

When you `ssh exe.dev`, our server decides based on the first key it receives whether it knows who you are. If we don't recognize the key, we ask you to register.

This is a pretty fundamental limitation of ssh.

If this is happening to you, options include:

- [specify a particular key to use with exe.dev](/docs/faq/ssh-key)
- add the other public keys to exe.dev using one of:
  - run `ssh-key add` in the repl ([docs](/docs/cli-ssh-key))
  - visit [your profile page](/user)
  - "re-register" with the new keys using your same email address
