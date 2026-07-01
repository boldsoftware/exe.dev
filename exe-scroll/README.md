# exe-scroll

`exe-scroll` is a small terminal session multiplexer in the spirit of
[dtach](https://github.com/crigler/dtach),
[abduco](https://github.com/martanne/abduco), and
[zmx](https://github.com/qbit/zmx), written in Zig. It hosts a PTY-backed
command behind a Unix socket so you can attach and detach without disturbing the
program — and it uses Ghostty's `ghostty-vt` terminal emulator to keep
scrollback, so reattaching repaints the screen (and optional history) exactly:
colors, styles, cursor and all.

## Usage

The default invocation takes a single argument — a path for the session socket
— and **attaches** to it if a live session is there, otherwise **creates** it:

```sh
# Create (no session yet): runs $SHELL behind /tmp/work/session.sock.
exe-scroll /tmp/work/session.sock

# ...later, from anywhere: attach to the same session.
exe-scroll /tmp/work/session.sock

# Create with an explicit command (everything after `--` is the command).
exe-scroll /tmp/work/session.sock -- bash -l
```

**Detach** (leaving the session running) by sending the attach client
`SIGUSR2`:

```sh
kill -USR2 <pid-of-exe-scroll>
```

The session keeps running; reattach later with the same command.

### Telling the processes apart

Each session involves a backgrounded *session server* (owns the pty and
scrollback) and one *attach client* per attachment. They rewrite their command
line so `ps` shows which is which:

```
$ ps -o pid,command
  4258 exe-scroll: attach /tmp/work/session.sock
  2976 exe-scroll: session /tmp/work/session.sock
```

So the `session` line is the one to send `SIGUSR1` (recreate socket), and an
`attach` line is the one to send `SIGUSR2` (detach).

### Options

| flag | meaning |
|------|---------|
| `-R none\|screen\|scrollback` | what to replay on attach (default `scrollback`) |
| `--version` | print version and exit |
| `-h`, `--help` | help |

Options may appear before or after the socket path.

## Design notes

- **Attach-or-create by default.** A single path argument is all you need; the
  tool connects if a session is live, otherwise spawns one. Intervening
  directories are created securely (mode `0700`).
- **A session server and attach clients.** Creating a session forks a
  backgrounded *session server* that owns the pty and the scrollback. Each
  `exe-scroll <socket>` you run is an *attach client* that connects to it over
  the socket and relays your terminal.
- **Signal-driven socket recreation (an abduco idea).** There's no polling
  timer and no file watcher. The session server understands `SIGUSR1`: on
  receipt, if its socket file is missing (or has been replaced), it binds a
  fresh one. So if a socket gets deleted out from under a live session,
  `kill -USR1 <server-pid>` brings it back and you can reattach. On exit the
  server only unlinks the socket if its on-disk inode still matches the one it
  created.
- **Ghostty for scrollback.** Every byte of program output is fed into an
  in-process `ghostty-vt` terminal. On attach the server serializes that
  emulator back to VT sequences (optionally including scrollback) so the client
  repaints exactly. `ghostty-vt` is imported as a Zig module directly — no
  C ABI / FFI. The session retains a fixed 1 MiB scrollback budget.
- **Simple, evolvable framing.** The server/client protocol is a trivial
  length-prefixed message format (`[type:u8][len:u32-le][payload]`), chosen to
  be easy to read, test and extend. There is intentionally no wire-protocol
  compatibility with dtach; unknown message types are ignored, so the protocol
  can grow.

## Building

exe-scroll imports Ghostty's `ghostty-vt` Zig module, so it builds against a
[Ghostty](https://github.com/ghostty-org/ghostty) checkout. You need Zig 0.15.2
(macOS also needs the Xcode command-line tools for Ghostty's bundled C++).

```sh
# Just build. Fetches Ghostty at the pinned revision into ./.ghostty for you:
make                                        # -> zig-out/bin/exe-scroll
make test                                   # build + run the test suite

# Already have a Ghostty checkout? Point at it to skip the fetch:
make GHOSTTY_SRC=/path/to/ghostty

# Reproducible static musl build for Linux (fetches the pinned toolchain via
# mise, and a pinned ghostty revision). Zig cross-compiles, so this runs on any
# host (Linux x86_64/arm64, macOS, ...) regardless of the target arch:
./build-static.sh amd64                     # or arm64

# Install straight into another tree (used by the top-level `make exe-scroll`,
# which bakes exe-scroll into the exelet rovol bin dir next to dtach):
OUT_DIR=/path/to/rovol ./build-static.sh amd64   # -> $OUT_DIR/bin/exe-scroll
```

The Zig toolchain version is pinned in `mise.toml` (with per-platform checksums
in `mise.lock`); `build-static.sh` uses [mise](https://mise.jdx.dev) to fetch it
consistently. Because Zig is a cross-compiler, `build-static.sh` emits a fully
static musl Linux binary for either arch from any build host — no Docker, buildx,
or qemu — which is what lets the exelet build produce it in place.

## Platforms

The source is POSIX and uses Zig's standard library for all the platform ABI
(termios, signals, ioctls, errno, ...), so it builds and runs natively on
Linux, macOS, and the BSDs. `build-static.sh` targets Linux: it produces a fully
static musl binary (for either arch, cross-compiled from any host). (Cross-
compiling the macOS binary *from* Linux is not set up here because Ghostty's
bundled C++ SIMD sources need the macOS SDK; build on the target OS, or wire up
an SDK, for a native macOS binary.)

## Testing

`test_e2e.zig` drives the real binary over PTYs — attach-or-create, secure
directory creation, detach (SIGUSR2) / reattach survival, replay modes, color
preservation, and the abduco-style socket recreation. Run it with Zig's test
harness:

```sh
zig build test     # builds the binary and runs the e2e tests against it
```

## License

MIT (see [LICENSE](LICENSE)). This is an independent implementation; it does not
reuse dtach's (GPL) source.
