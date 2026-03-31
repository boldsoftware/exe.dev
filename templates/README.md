# Templates

HTML templates rendered by the web server and proxy.

## Proxy templates (`proxy-*.html`)

Templates prefixed with `proxy-` are rendered on **VM subdomains**
(e.g. `mybox.exe.xyz`), not on the main `exe.dev` domain.

These templates **must be fully self-contained**: all CSS inlined in a
`<style>` tag, no `<link rel="stylesheet">`, no `<script src="...">`. 
The only external resources allowed are `<link rel="icon">` and
`<link rel="apple-touch-icon">` (favicons degrade gracefully).

### Why

On VM subdomains, all HTTP requests are either authenticated and proxied
to the container, or rejected. The proxy has no `/static/` route — any
request for `/static/common.css` would either:

1. Get auth-redirected (if unauthenticated), or
2. Get forwarded to the container (which doesn't have our CSS)

Since proxy-rendered pages (503, access denied, port unbound, etc.) are
shown precisely when normal request handling has failed, they cannot
depend on any external resources loading successfully.

### Enforced by test

`TestProxyTemplatesAreSelfContained` in `proxy_selfcontained_test.go`
verifies that all `proxy-*` templates have no external stylesheet or
script references and contain an inline `<style>` block.
