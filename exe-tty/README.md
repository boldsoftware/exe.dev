# exe-tty

`exe-tty` serves a web-based terminal. Each tab is an
[exe-scroll](../exe-scroll) session, so you can connect to the same session
from multiple browsers.

Anyone who can access this web app has a shell to your computer! The default
security posture is to require a token which sets a cookie; this may not
be an appropriate security posture for your use case.

## Usage

Download `exe-tty-<os>-<arch>` from the
[releases](https://github.com/boldsoftware/exe.dev/releases?q=exe-tty), then:

```sh
$ mv exe-tty-* exe-tty && chmod +x exe-tty
$ xattr -d com.apple.quarantine exe-tty   # macOS only
$ ./exe-tty
exe-tty v0.1.9...: http://localhost:7600/login?token=5001fa...
```

Sessions live in `${TMPDIR:-/tmp}/exe-tty-$(id -u)/<name>.sock`. You can attach
to one from any other terminal too:

```sh
exe-tty exe-scroll ${TMPDIR:-/tmp}/exe-tty-$(id -u)/ruby.sock
```

## See Also

See the blog post [Ghostty in the Machine: the Saga of exe-scroll](https://blog.exe.dev/exe-scroll) for
some background on how `exe-scroll` works.

You can find VMs to run this on at [exe.dev](https://exe.dev/)!

## Development

Needs [mise](https://mise.jdx.dev/), which installs the pinned Go, Node.js, and
pnpm.

```sh
make
make test
```