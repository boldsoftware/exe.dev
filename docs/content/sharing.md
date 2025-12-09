---
title: Sharing
description: share it like it's hot
subheading: "2. Features"
suborder: 3
---

You can share your box's HTTP port (see [the http proxy documentation](./proxy))
with your friends. There are three mechanisms:

1. Make the HTTP proxy public with `share set-public <box>`. To point the proxy
   at a different port inside the box, run `share port <box> <port>` first.
   Marking it public lets anyone access the server without logging in.

2. Add specific e-mail addresses using `share add <box> <email>`. This will
send the recipient an e-mail. They can then log into exe.dev with that e-mail,
and access `https://box.exe.xyz/`.

3. Create a share link with `share add-link <box>`. The generated
link will allow anyone access to the page, after they register and login.
Revoking the link (which can be done with the `remove-link` command)
does not revoke their access, but you can remove users who are already
part of the share using `share remove <box> <email>`.

When you share a box, users will see your email address.
