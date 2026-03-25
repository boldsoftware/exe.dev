---
title: HTTP Proxy Integration
description: Inject headers into HTTP requests from your VM
subheading: "3. Integrations"
suborder: 2
published: true
---

The HTTP Proxy integration serves as an HTTP(S) proxy that injects a header
into your request. This can be useful to interact with an API that requires
a bearer token.

For example, the following snippet creates, attaches, and uses the http proxy
integration to inject a header into a request.

```
exe.dev ▶ integrations add http-proxy --name mirror --target https://httpbin.org/ --header prettiest-of-them-all:me --attach vm:my-vm-name
Added integration mirror

Usage from a VM:
  ssh my-vm-name curl http://mirror.int.exe.xyz/

exe.dev ▶ ssh my-vm-name curl -s http://mirror.int.exe.xyz/anything -Hfoo:bar
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
