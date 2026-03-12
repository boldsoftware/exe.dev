---
title: Integrations
description: Integrate exe.dev with other tools and services
subheading: "9. Integrations"
preview: true
---

Integrations connect your exe.dev VM to other services securely and flexibly.
They allow you to "inject secrets" on the network, so that those secrets cannot
be extracted from the VM itself. Integrations are created with the `integrations new`
command and attached to VMs with the `integrations attach` command.

## HTTP Proxy Integration

The HTTP Proxy integration serves as an HTTP(S) proxy that injects a header
into your request. This can be useful to interact with an API that requires
a bearer token.

For example, the following snippet creates, attaches, and uses the http proxy
integration to inject a header into a request.

```
integrations new http-proxy --name httpbin --target https://httpbin.org/ --header example:indeed
integrations attach httpbin vm:my-vm
ssh my-vm curl http://httpbin.int.exe.xyz/anything
TODO show output
```

The HTTP Proxy integration supports HTTP basic auth as well.

## GitHub integration

Because the GitHub personal access token [link to github pat instructions] instructions are a bit
much, we offer a first class GitHub integration.

You must first connect to your GitHub account with:

```
integrations setup github
...
```

Once you've established the link, you can create per-repo integrations as follows:

```
integrations new github --name blog --repository ghuser/blog
integrations attach blog myvm
ssh myvm git init
ssh myvm git fetch http://blog.int.exe.xyz/ghuser/blog.git
```
