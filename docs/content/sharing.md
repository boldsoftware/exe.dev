---
title: Sharing
description: share it like it's hot
subheading: "2. Features"
suborder: 2
---

You can share your VM's HTTP port (see [the http proxy documentation](./proxy))
with your friends. There are three mechanisms:

1. Make the HTTP proxy public with `share set-public <vm>`. To point the proxy
   at a different port inside the VM, run `share port <vm> <port>` first.
   Marking it public lets anyone access the server without logging in.

2. Add specific e-mail addresses using `share add <vm> <email>`. This will
send the recipient an e-mail. They can then log into exe.dev with that e-mail,
and access `https://vmname.exe.xyz/`.

3. Create a share link with `share add-link <vm>`. The generated
link will allow anyone access to the page, after they register and login.
Revoking the link (which can be done with the `remove-link` command)
does not revoke their access, but you can remove users who are already
part of the share using `share remove <vm> <email>`.

When you share a VM, users will see your email address.
