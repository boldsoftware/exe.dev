# The exe-tty API

This is the protocol between the web terminal in [`web/`](web) and a server,
such as the `exe-tty` command in this directory. All paths are relative to the
page's origin.

How a server authenticates the browser is up to it. `exe-tty` uses a cookie
(see [README.md](README.md)); requests without it get `401`.

## Sessions

A session is a shell, named by the client with a short `[A-Za-z0-9-]+`. It
keeps running while no one is attached, and any number of browser tabs can
attach to it at once. It ends when its shell exits or a client
closes it; the next attach to the name starts a new shell.

### `GET /`

The web app.

### `GET /terminal/sessions`

```json
{"sessions": [{"name": "ruby"}, {"name": "amber"}]}
```

The live sessions, oldest first. If this answers `401` or `403`, or redirects,
the app reloads the page so the server can send the user to log in.

### `POST /terminal/sessions/close`

Form body `name=<session>`. Ends the session, and answers `204`. Clients
attached to it are disconnected with code `4001` (below).

### `POST /terminal/upload?filename=<name>&dir=<dir>`

The request body is the file's contents. The server writes it into `dir`
(default `/tmp`; `~` and relative paths are in the user's home directory),
cleaning up `filename` to `[A-Za-z0-9._-]` and adding a suffix rather than
replacing an existing file. It answers `200 {"path": "<where it went>"}`, or an
error status with `{"error": "<message>"}`; `413` means the file was too big.

Servers reject POSTs from other origins with `403`.

## `GET /terminal/ws/<tab>?name=<session>&d=<dir>` (WebSocket)

Attaches to session `name`, starting it if it isn't running. `<tab>` is an
opaque tab id; servers may ignore it. `d` is the directory a new session's
shell starts in (as for upload's `dir`; default the home directory). `cols`
and `rows`, if present, give a new session's initial size.

The shell's `$TERM` is `xterm-256color`.

Every message is a JSON text frame. From the client:

```js
{"type": "input", "data": "ls\r"}                 // keystrokes, as UTF-8 text
{"type": "resize", "cols": 120, "rows": 40}       // the terminal's size
{"type": "ping"}                                  // are you there?
```

Client messages may be at most 32 KiB, so the client splits long pastes into
several `input` messages. `resize` may also carry `"term"`, the `$TERM` the
client would prefer. Servers may ignore it.

From the server:

```js
{"type": "output", "data": "bHMNCg=="}  // shell output, base64-encoded bytes
{"type": "pong"}                        // answers each ping and resize
```

The client sends `resize` as soon as the socket opens and whenever its size
changes. On attach, the server sends output that redraws the screen, so
reconnecting shows what was there.

The client sends `ping` when it suspects the connection is dead (say, after
the laptop wakes up). `ping` must not have any effect on the shell. If nothing
comes back, the client tries once more with a pair of resizes (one row
taller, then back) for servers that predate `ping`, and if that gets nothing
either, it reconnects.

When the server closes the socket with code **4001**, the session has ended
and the client offers to start a new one. On any other close, the client
reconnects (with backoff) to the same session.
