---
title: Integrations
description: Integrate exe.dev with other tools and services
subheading: "9. Integrations"
preview: true
---

Integrations connect your exe.dev VM to other services securely and flexibly.
They allow you to "inject secrets" on the network, so that those secrets cannot
be extracted from the VM itself. Integrations are created with the `integrations add`
command and attached to VMs with the `integrations attach` command.

## HTTP Proxy Integration

The HTTP Proxy integration serves as an HTTP(S) proxy that injects a header
into your request. This can be useful to interact with an API that requires
a bearer token.

For example, the following snippet creates, attaches, and uses the http proxy
integration to inject a header into a request.

```
exe.dev ▶ integrations add http-proxy --name mirror --target https://httpbin.org/ --header prettiest-of-them-all:me --attach vm:my-vm-name
Added integration mirror

Usage from a VM:
  ssh my-vm-name curl http://mirror.int.exe-staging.xyz/

exe.dev ▶ ssh my-vm-name curl -s http://mirror.int.exe-staging.xyz/anything -Hfoo:bar
{
  "args": {},
  "data": "",
  "files": {},
  "form": {},
  "headers": {
    "Accept": "*/*",
    "Accept-Encoding": "gzip",
    "Foo": "bar",
    "Host": "httpbin.org",
    "Prettiest-Of-Them-All": "me",
    "User-Agent": "curl/8.5.0",
    "X-Amzn-Trace-Id": "Root=1-69b339a2-0032d20f5263c6dc17235289"
  },
  "json": null,
  "method": "GET",
  "origin": "64.34.88.25",
  "url": "https://httpbin.org/anything"
}
```

The HTTP Proxy integration supports HTTP basic auth as well.

## GitHub Integration

Instead of [setting up a GitHub personal access token](faq/github-token),
the GitHub integration connects your GitHub account to exe.dev so that you
can work on private repos without managing tokens, and without having
tokens on the VM itself.

First, link your GitHub account:

```
exe.dev ▶ integrations setup github
Authorize your GitHub account:
  https://exe.dev/r/abc123...

Waiting...
Connected: your-github-user
```

The command prints a URL. Open it in a browser where you are logged into
GitHub and authorize the exe.dev GitHub App. Once authorized, the
terminal unblocks and confirms the connection.

You can verify the connection at any time:

```
exe.dev ▶ integrations setup github --verify
✓ your-github-user (installed on your-github-user) — verified (API user: your-github-user)
```

Once connected, create per-repo integrations:

```
exe.dev ▶ integrations add github --name blog --repository ghuser/blog --attach vm:my-vm
Added integration blog

Usage from a VM:
  ssh my-vm 'cd $(mktemp -d) && git clone https://blog.int.exe.xyz/ghuser/blog.git'
```

Then from inside the VM:

```
git clone https://blog.int.exe.xyz/ghuser/blog.git
```

Only the specific repository you configured is accessible through the
integration, and the credentials never appear inside the VM.

To disconnect your GitHub account:

```
exe.dev ▶ integrations setup github -d
Disconnected GitHub: your-github-user (your-github-user)
```
