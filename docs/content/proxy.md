---
title: exe.dev HTTP Proxies
description: Configure and use the HTTP proxies provided for exe.dev containers.
subheading: Operations
published: false
---

# exe.dev HTTP Proxies

exe.dev proxies traffic to https://boxname.exe.dev/ to your box seamlessly, handling
certificates and TLS termination.

## Configuring which port to proxy

If your container exposes a port in its container definition, port 80, or the lowest
TCP port above 1024 will be chosen. Otherwise, we proxy port 80 by default. You can
change the port chosen with `ssh exe.dev route <boxname> --port=<port> --private" command.

## Private vs Public Proxies

By default, only users with access to the box can access the HTTP proxy.
Users accessing https://boxname.exe.dev/ for the first time will be
redirected to an authentication flow.

To share your site publically, run `ssh exe.dev route <boxname> --port=<port> --public`.

## Share Links (not yet implemented TODO)

Create a share link to your box's HTTP service by running
`ssh exe.dev share link`. Manage share links with
`ssh exe.dev share ls` and `ssh exe.dev share rm <link>`.

Users with share links can access the page.

TODO: Add e-mail shares?

## Using exe.dev authentication

If you would like to impelement authorization in your service,
you can leverage exe.dev's existing authentication. You can
look for X-exedev-userid headers in the HTTP requests. If they are
not set, you can redirect users to https://exe.dev/auth_redirect?.... TODO TODO
to log in.

For example, the following nginx configuration would allow
only the specified e-mail addresses to see the page.

## Exposing additional ports

For ports 2000, 3000, 4000, [TODO], you can also access https://boxname.exe.dev:8000/ (and similar)
to access the given port directly. exe.dev terminates TLS and proxies to your HTTP service.
These non-default port shares are always private, and only users with access to the box can
access them.
