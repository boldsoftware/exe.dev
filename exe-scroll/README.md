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

### Multiple clients: attaching or typing claims the size

Any number of clients can attach to one session, and their windows rarely
agree on a size. Rather than letting every resize fight over the PTY
(last-write-wins), the session tracks a *size owner*: the client whose window
the PTY follows. A client that **attaches** or **types** claims ownership and
applies its size; resizes from other clients are recorded but not applied
until one of them does the same. Attaching claims because a client narrower
than the PTY is unusable — programs lay out text for the PTY's width — so the
terminal you just opened should fit; a wider bystander merely sees a narrower
layout until it types. Only a client that has advertised a real (nonzero)
size can own. Terminal auto-replies that share the input path (focus reports,
cursor position / device attribute query responses, and the like) never claim
ownership, and neither does mouse-wheel scrolling — clicking does. When the
owner detaches the size is unowned and the next resize (or keystroke) claims
it — unless exactly one attached client remains, in which case the PTY snaps
to its size immediately. With a single client attached, resizes always apply
immediately.

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
| `--version` | print the source version, `0.<commits>.9<tree>`, and exit (see `sourceVersion` in build.zig) |
| `-h`, `--help` | help |

Options may appear before or after the socket path.

## Building

```sh
make                                        # -> zig-out/bin/exe-scroll
make test                                   # build + run the test suite
```

## License

MIT (see [LICENSE](LICENSE)).
