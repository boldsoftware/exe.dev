#!/usr/bin/env python3
"""An SSH-ish interactive terminal for an exe.dev VM, over HTTPS (no port 22).

Usage:
    experimental-exe-ssh.py <vm> [session-name]   attach (session-name defaults
                                                  to a random color name)
    experimental-exe-ssh.py <vm> --list           list existing sessions

The session is persistent and the client auto-reconnects, so dropping the
network (or closing your laptop) reattaches you to the same shell. A dropped
link is detected within a few seconds via WebSocket keepalive pings.

With no session-name, a new random color-name session is created; pass an
explicit name to reattach to an existing one, or use --list to see what's
running.

Like ssh(1), a few escape sequences are recognized right after Enter:
  ~.   terminate the connection
  ~^Z  suspend exe-ssh (resume with 'fg')
  ~~   send a literal '~'
  ~?   list these sequences
"""

# Standard library only!
import argparse
import base64
import errno
import hashlib
import json
import os
import random
import re
import select
import signal
import socket
import ssl
import struct
import subprocess
import sys
import termios
import time
import tty
from urllib.parse import urlencode

TOKEN_TTL_SECONDS = 300

# Liveness probing. A silently-dropped link (laptop sleep, Wi-Fi roam, NAT
# idle-timeout) sends no TCP FIN/RST, so select() never wakes and an idle
# shell's writes don't fail for minutes. We instead send our own WebSocket
# pings and treat a too-long silence as a dead link, so reconnect is fast.
PING_INTERVAL = 4.0   # send a ping this often when otherwise idle
DEAD_AFTER = 12.0     # no bytes at all for this long => assume the link died
CONNECT_TIMEOUT = 10  # TCP connect + WebSocket handshake budget

# Session names are drawn from this palette of color names; a new session gets
# a random one.
SESSION_COLORS = [
    "amber", "aqua", "azure", "beige", "bisque", "black", "blue", "bronze", "brown", "coral",
    "cream", "crimson", "cyan", "denim", "ebony", "emerald", "fuchsia", "gold", "gray", "green",
    "indigo", "ivory", "jade", "khaki", "lavender", "lemon", "lilac", "lime", "linen", "magenta",
    "maroon", "mauve", "mint", "navy", "ochre", "olive", "orange", "orchid", "peach", "pearl",
    "periwinkle", "pink", "plum", "purple", "red", "rose", "ruby", "rust", "saffron", "sage",
    "salmon", "sand", "sapphire", "scarlet", "sepia", "silver", "slate", "snow", "tan", "teal",
    "thistle", "tomato", "turquoise", "umber", "vanilla", "violet", "wheat", "white", "wine", "yellow",
    "amethyst", "apricot", "ash", "auburn", "buff", "cardinal", "carmine", "cerise", "charcoal", "cherry",
    "chestnut", "chocolate", "cinnamon", "claret", "cobalt", "copper", "cornflower", "eggplant", "flax", "garnet",
    "ginger", "goldenrod", "gunmetal", "hazel", "honeydew", "ice", "iron", "jasmine", "jet", "mango",
]


def random_session_name(avoid=()):
    """Pick a random color-name session, preferring names not already in
    `avoid` so a fresh session doesn't collide with an existing one; only if
    every color is taken do we fall back to any color."""
    avoid = set(avoid)
    available = [n for n in SESSION_COLORS if n not in avoid]
    return random.choice(available or SESSION_COLORS)


# ANSI colors for status banners. Suppressed when stderr isn't a tty (e.g.
# piped or captured), so logs stay plain there.
GREEN = "\x1b[32m"
YELLOW = "\x1b[33m"
_RESET = "\x1b[0m"


def log(msg):
    sys.stderr.write("\r\x1b[2K" + msg + "\r\n")
    sys.stderr.flush()


def banner(msg, color=""):
    """A status line that stands out from shell output: bold, reverse-video,
    and color when stderr is a terminal. Used for reconnection state so the
    user can plainly see the link dropped and came back."""
    if color and sys.stderr.isatty():
        msg = f"{color}\x1b[1m\x1b[7m exe-ssh: {msg} {_RESET}"
    else:
        msg = f"exe-ssh: {msg}"
    log(msg)


def b64url(data: bytes) -> str:
    """base64url without padding (RFC 4648 section 5)."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def find_key(explicit, host):
    """Resolve the SSH private key to sign with.

    With no -i/--key, ask ssh(1) itself which identity it would use for the
    box host (`ssh -G`), honoring the user's ssh_config, and pick the first
    candidate that exists on disk.
    """
    if explicit:
        path = os.path.expanduser(explicit)
        if not os.path.exists(path):
            sys.exit(f"error: ssh key not found: {path}")
        return path
    try:
        out = subprocess.check_output(["ssh", "-G", host], text=True)
    except FileNotFoundError:
        sys.exit("error: ssh not found on PATH")
    except subprocess.CalledProcessError as e:
        sys.exit(f"error: ssh -G {host} failed: {e}")
    for line in out.splitlines():
        if line.lower().startswith("identityfile "):
            path = os.path.expanduser(line.split(None, 1)[1].strip())
            if os.path.exists(path):
                return path
    sys.exit("error: no SSH key found; pass -i/--key, or create and register one "
             "(ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519, then ssh exe.dev ssh-key add)")


def generate_token(key_path, namespace):
    """Mint a short-lived exe0 API token by SSH-signing a permissions blob.

    See https://exe.dev/docs/https-api-local-key.
    """
    perms = {"exp": int(time.time()) + TOKEN_TTL_SECONDS}
    payload = json.dumps(perms, separators=(",", ":")).encode("utf-8")

    try:
        armored = subprocess.check_output(
            ["ssh-keygen", "-Y", "sign", "-f", key_path, "-n", namespace],
            input=payload, stderr=subprocess.PIPE,
        ).decode("ascii")
    except FileNotFoundError:
        sys.exit("error: ssh-keygen not found on PATH")
    except subprocess.CalledProcessError as e:
        sys.exit(f"error: ssh-keygen sign failed: {e.stderr.decode().strip()}")

    # Strip the PEM armor (-----BEGIN/END SSH SIGNATURE-----) and decode.
    lines = [ln for ln in armored.splitlines() if ln and not ln.startswith("-----")]
    sig_der = base64.b64decode("".join(lines))

    return "exe0." + b64url(payload) + "." + b64url(sig_der)


class WSError(Exception):
    pass


class WebSocket:
    """Minimal RFC 6455 WebSocket client (text frames only, with masking)."""

    OP_CONT = 0x0
    OP_TEXT = 0x1
    OP_BINARY = 0x2
    OP_CLOSE = 0x8
    OP_PING = 0x9
    OP_PONG = 0xA

    # Application-private close code (RFC 6455 4000-4999) the exe.dev terminal
    # server sends when the remote shell itself exits (Ctrl-D / logout), as
    # opposed to a generic close (server shutdown, error) we should retry.
    CLOSE_SHELL_EXITED = 4001

    GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

    def __init__(self, sock):
        self.sock = sock
        self._buf = b""
        # State for reassembling fragmented (continuation) messages.
        self._frag_opcode = None
        self._frag_payload = bytearray()

    @classmethod
    def connect(cls, host, port, path, headers, insecure, plaintext=False,
                connect_host=None, timeout=CONNECT_TIMEOUT):
        # connect_host lets us TCP-connect somewhere (e.g. localhost) while
        # still presenting `host` in the Host header / SNI / Authorization.
        raw = socket.create_connection((connect_host or host, port), timeout=timeout)
        # Belt-and-suspenders: ask the kernel to probe dead peers too. Our own
        # application-level pings are the primary signal, but TCP keepalive
        # also tears down a half-open socket the OS can detect.
        try:
            raw.setsockopt(socket.SOL_SOCKET, socket.SO_KEEPALIVE, 1)
            for opt, val in (("TCP_KEEPIDLE", 5), ("TCP_KEEPINTVL", 3),
                             ("TCP_KEEPCNT", 3)):
                if hasattr(socket, opt):
                    raw.setsockopt(socket.IPPROTO_TCP, getattr(socket, opt), val)
        except OSError:
            pass
        if plaintext:
            sock = raw
        else:
            ctx = ssl.create_default_context()
            if insecure:
                ctx.check_hostname = False
                ctx.verify_mode = ssl.CERT_NONE
            sock = ctx.wrap_socket(raw, server_hostname=host)

        key = base64.b64encode(os.urandom(16)).decode("ascii")
        req = [
            f"GET {path} HTTP/1.1",
            f"Host: {host}",
            "Upgrade: websocket",
            "Connection: Upgrade",
            f"Sec-WebSocket-Key: {key}",
            "Sec-WebSocket-Version: 13",
        ]
        for k, v in headers.items():
            req.append(f"{k}: {v}")
        req.append("\r\n")
        sock.sendall("\r\n".join(req).encode("ascii"))

        ws = cls(sock)
        status_line, resp_headers = ws._read_handshake()
        parts = status_line.split(" ", 2)
        if len(parts) < 2 or parts[1] != "101":
            sock.close()
            raise WSError(f"handshake failed: {status_line}")

        # Verify Sec-WebSocket-Accept (RFC 6455 4.1) so we don't speak the
        # WebSocket framing protocol to a server that didn't actually agree.
        expect = base64.b64encode(
            hashlib.sha1((key + cls.GUID).encode("ascii")).digest()
        ).decode("ascii")
        got = None
        for h in resp_headers:
            name, _, val = h.partition(":")
            if name.strip().lower() == "sec-websocket-accept":
                got = val.strip()
        if got != expect:
            sock.close()
            raise WSError("handshake failed: bad Sec-WebSocket-Accept")

        ws.sock.setblocking(False)
        return ws

    def _read_handshake(self):
        # Read until end of HTTP headers (CRLFCRLF).
        while b"\r\n\r\n" not in self._buf:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise WSError("connection closed during handshake")
            self._buf += chunk
        head, self._buf = self._buf.split(b"\r\n\r\n", 1)
        lines = head.decode("latin-1").split("\r\n")
        return lines[0], lines[1:]

    def fileno(self):
        return self.sock.fileno()

    def send_text(self, data: str):
        self._send_frame(self.OP_TEXT, data.encode("utf-8"))

    def _send_frame(self, opcode, payload: bytes):
        b1 = 0x80 | opcode  # FIN + opcode
        header = bytearray([b1])
        n = len(payload)
        mask_bit = 0x80  # client frames must be masked
        if n < 126:
            header.append(mask_bit | n)
        elif n < 65536:
            header.append(mask_bit | 126)
            header += struct.pack("!H", n)
        else:
            header.append(mask_bit | 127)
            header += struct.pack("!Q", n)
        mask = os.urandom(4)
        header += mask
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self._send_all(bytes(header) + masked)

    def _send_all(self, data):
        # Socket is non-blocking; loop until everything is flushed.
        view = memoryview(data)
        while view:
            try:
                sent = self.sock.send(view)
                if sent == 0:
                    raise WSError("connection closed while sending")
                view = view[sent:]
            except ssl.SSLWantWriteError:
                select.select([], [self.sock], [], 1)
            except (ssl.SSLWantReadError,):
                select.select([self.sock], [], [], 1)
            except BlockingIOError:
                select.select([], [self.sock], [], 1)

    def recv_into_buffer(self):
        """Pull available bytes off the socket. Returns False on EOF.

        For TLS sockets a single TCP read can decrypt into more buffered
        application data than one recv() returns; select() on the raw fd
        wouldn't fire again, so drain ssl pending() until it's empty.
        """
        try:
            chunk = self.sock.recv(65536)
        except ssl.SSLWantReadError:
            return True
        except BlockingIOError:
            return True
        if not chunk:
            return False
        self._buf += chunk
        pending = getattr(self.sock, "pending", None)
        if pending is not None:
            while pending() > 0:
                try:
                    more = self.sock.recv(65536)
                except (ssl.SSLWantReadError, BlockingIOError):
                    break
                if not more:
                    break
                self._buf += more
        return True

    def frames(self):
        """Yield (opcode, payload) for each complete *message*.

        Reassembles RFC 6455 fragmented messages (FIN=0 + continuation
        frames) so callers always see whole text/binary messages. Control
        frames (ping/pong/close) are never fragmented and pass through.
        """
        while True:
            parsed = self._parse_one()
            if parsed is None:
                return
            fin, opcode, payload = parsed
            if opcode in (self.OP_PING, self.OP_PONG, self.OP_CLOSE):
                yield opcode, payload
                continue
            if opcode == self.OP_CONT:
                if self._frag_opcode is None:
                    raise WSError("unexpected continuation frame")
                self._frag_payload += payload
                if fin:
                    op, data = self._frag_opcode, bytes(self._frag_payload)
                    self._frag_opcode = None
                    self._frag_payload = bytearray()
                    yield op, data
                continue
            # New data frame (TEXT/BINARY).
            if fin:
                yield opcode, payload
            else:
                self._frag_opcode = opcode
                self._frag_payload = bytearray(payload)

    def _parse_one(self):
        buf = self._buf
        if len(buf) < 2:
            return None
        b0, b1 = buf[0], buf[1]
        fin = bool(b0 & 0x80)
        opcode = b0 & 0x0F
        masked = b1 & 0x80
        length = b1 & 0x7F
        idx = 2
        if length == 126:
            if len(buf) < idx + 2:
                return None
            (length,) = struct.unpack("!H", buf[idx:idx + 2])
            idx += 2
        elif length == 127:
            if len(buf) < idx + 8:
                return None
            (length,) = struct.unpack("!Q", buf[idx:idx + 8])
            idx += 8
        mask = b""
        if masked:
            if len(buf) < idx + 4:
                return None
            mask = buf[idx:idx + 4]
            idx += 4
        if len(buf) < idx + length:
            return None
        payload = bytearray(buf[idx:idx + length])
        if masked:
            for i in range(length):
                payload[i] ^= mask[i % 4]
        self._buf = buf[idx + length:]
        return fin, opcode, bytes(payload)

    def ping(self, payload=b""):
        self._send_frame(self.OP_PING, payload)

    def pong(self, payload):
        self._send_frame(self.OP_PONG, payload)

    def close(self):
        try:
            self._send_frame(self.OP_CLOSE, b"")
        except Exception:
            pass
        try:
            self.sock.close()
        except Exception:
            pass


def http_get_json(host, port, path, headers, insecure, plaintext=False,
                  connect_host=None, timeout=CONNECT_TIMEOUT):
    """Minimal blocking HTTP/1.1 GET that returns parsed JSON.

    Shares the WebSocket client's connection conventions (connect_host/SNI/
    plaintext). Used for the sessions-list endpoint, which is a plain GET
    rather than a WebSocket upgrade.

    Deliberately tiny: handles Content-Length or Connection: close framing,
    with hard caps on header/body size. It does NOT implement chunked transfer
    encoding -- we send Connection: close so a well-behaved HTTP/1.1 server
    delimits by EOF instead; if a response is chunked anyway we fail loudly
    rather than mis-parse.
    """
    # We interpolate host/path into the raw request line, so reject anything
    # that could smuggle extra headers. (Callers already validate these, but
    # this is the security-sensitive choke point.)
    for label, val in (("host", host), ("path", path)):
        if "\r" in val or "\n" in val:
            raise WSError(f"refusing to send {label} with embedded newline")
    max_header = 64 * 1024
    max_body = 4 * 1024 * 1024

    raw = socket.create_connection((connect_host or host, port), timeout=timeout)
    try:
        if plaintext:
            sock = raw
        else:
            ctx = ssl.create_default_context()
            if insecure:
                ctx.check_hostname = False
                ctx.verify_mode = ssl.CERT_NONE
            sock = ctx.wrap_socket(raw, server_hostname=host)
    except Exception:
        raw.close()
        raise
    try:
        req = [f"GET {path} HTTP/1.1", f"Host: {host}", "Connection: close",
               "Accept: application/json"]
        for k, v in headers.items():
            req.append(f"{k}: {v}")
        req.append("\r\n")
        sock.sendall("\r\n".join(req).encode("ascii"))

        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = sock.recv(4096)
            if not chunk:
                raise WSError("connection closed during response headers")
            buf += chunk
            if len(buf) > max_header:
                raise WSError("response headers too large")
        head, body = buf.split(b"\r\n\r\n", 1)
        lines = head.decode("latin-1").split("\r\n")
        status = lines[0].split(" ", 2)
        if len(status) < 2 or not status[1].isdigit():
            raise WSError(f"malformed status line: {lines[0]!r}")
        code = int(status[1])

        length = None
        for h in lines[1:]:
            n, _, v = h.partition(":")
            n = n.strip().lower()
            if n == "transfer-encoding" and "chunked" in v.lower():
                raise WSError("chunked transfer encoding not supported")
            if n == "content-length":
                try:
                    length = int(v.strip())
                except ValueError:
                    length = None

        # Honor Content-Length when present; otherwise read to EOF (we asked
        # for Connection: close, so the server closes when done).
        while length is None or len(body) < length:
            chunk = sock.recv(4096)
            if not chunk:
                break
            body += chunk
            if len(body) > max_body:
                raise WSError("response body too large")
        if length is not None:
            body = body[:length]
    finally:
        try:
            sock.close()
        except Exception:
            pass

    if code != 200:
        raise WSError(f"HTTP {code}: {body.decode('utf-8', 'replace').strip()}")
    return json.loads(body.decode("utf-8"))


# ---------------------------------------------------------------------------
# Terminal session
# ---------------------------------------------------------------------------


def term_size(fd):
    try:
        cols, rows = os.get_terminal_size(fd)
        return rows, cols
    except OSError:
        return 24, 80


# The escape character, like ssh(1), is only recognized right after a newline
# (start of line). "~?" lists the supported sequences.
ESCAPE_CHAR = ord("~")
ESCAPE_HELP = (
    "\r\nSupported escape sequences (recognized after Enter):\r\n"
    "  ~.   terminate the connection\r\n"
    "  ~^Z  suspend exe-ssh\r\n"
    "  ~~   send a literal '~'\r\n"
    "  ~?   this message\r\n"
)


class EscapeFilter:
    """Recognize ssh-style ``~`` escape sequences in the local input stream.

    Fed raw bytes from the tty, it returns the bytes that should actually be
    forwarded to the remote shell and, separately, a list of local actions
    ("quit", "suspend", "help") triggered by escape sequences. As in ssh(1),
    the escape char is only special at the start of a line.
    """

    def __init__(self):
        # Start-of-connection counts as start-of-line, matching ssh.
        self.at_line_start = True
        self.after_escape = False

    def feed(self, data: bytes):
        out = bytearray()
        actions = []
        for byte in data:
            if self.after_escape:
                self.after_escape = False
                if byte == ESCAPE_CHAR:
                    out.append(ESCAPE_CHAR)        # ~~ -> literal ~
                elif byte == ord("."):
                    actions.append("quit")
                elif byte == 0x1A:                 # ~^Z -> suspend
                    actions.append("suspend")
                elif byte == ord("?"):
                    actions.append("help")
                else:
                    # Not a recognized command: pass the ~ and this byte through.
                    out.append(ESCAPE_CHAR)
                    out.append(byte)
                self.at_line_start = byte in (0x0D, 0x0A)
                continue
            if self.at_line_start and byte == ESCAPE_CHAR:
                # Hold the ~ until we see the next byte to decide.
                self.after_escape = True
                self.at_line_start = False
                continue
            out.append(byte)
            self.at_line_start = byte in (0x0D, 0x0A)
        return bytes(out), actions


def run_session(ws, stdin_fd, old_termios):
    """Pump bytes between the local tty (stdin_fd) and the websocket.

    Returns a reason string describing why the session ended:
      "ended"        -- the remote shell exited (server closed with
                        CLOSE_SHELL_EXITED, e.g. you hit Ctrl-D / typed exit),
                        local stdin reached EOF, or the user asked to quit with
                        the ``~.`` escape. The caller should NOT reconnect.
      "disconnected" -- the connection dropped (TCP EOF, an abnormal close, or
                        our keepalive ping went unanswered for too long). The
                        caller should reconnect.

    ``old_termios`` is the cooked-mode terminal state, restored around a
    ``~^Z`` suspend so the user's job-control shell behaves normally.
    """
    rows, cols = term_size(stdin_fd)
    ws.send_text(json.dumps({"type": "resize", "rows": rows, "cols": cols,
                             "term": os.environ.get("TERM", "xterm-256color")}))

    winch = {"flag": False}

    def on_winch(signum, frame):
        winch["flag"] = True

    old_winch = signal.signal(signal.SIGWINCH, on_winch)
    escapes = EscapeFilter()
    # Liveness tracking: any inbound byte (data, pong, ping) proves the link is
    # alive. If nothing arrives for DEAD_AFTER seconds despite our pings, the
    # link is wedged -- bail out and reconnect rather than waiting for a TCP
    # write to eventually fail.
    last_recv = time.monotonic()
    last_ping = last_recv
    try:
        while True:
            if winch["flag"]:
                winch["flag"] = False
                rows, cols = term_size(stdin_fd)
                ws.send_text(json.dumps({"type": "resize", "rows": rows, "cols": cols}))

            try:
                readable, _, _ = select.select([stdin_fd, ws], [], [], 1)
            except (select.error, InterruptedError) as e:
                if getattr(e, "errno", None) == errno.EINTR or (e.args and e.args[0] == errno.EINTR):
                    continue
                raise

            now = time.monotonic()

            # Process anything the server sent FIRST, so a pending close frame
            # (especially CLOSE_SHELL_EXITED) is never misread as a dead link by
            # the keepalive check below, and so inbound bytes refresh liveness.
            if ws in readable:
                if not ws.recv_into_buffer():
                    return "disconnected"  # TCP EOF: treat as a dropped link
                last_recv = now
                for opcode, payload in ws.frames():
                    if opcode == WebSocket.OP_CLOSE:
                        # The remote shell exited (Ctrl-D / logout) only when
                        # the server says so with its dedicated close code. Any
                        # other code (generic close, server shutdown, error) or
                        # none means we should recover by reconnecting.
                        code = (payload[0] << 8) | payload[1] if len(payload) >= 2 else None
                        if code == WebSocket.CLOSE_SHELL_EXITED:
                            return "ended"
                        return "disconnected"
                    if opcode == WebSocket.OP_PING:
                        ws.pong(payload)
                        continue
                    if opcode == WebSocket.OP_PONG:
                        continue  # liveness already credited via last_recv
                    if opcode in (WebSocket.OP_TEXT, WebSocket.OP_BINARY):
                        handle_server_message(payload)

            # Keepalive: declare a silent link dead, else ping if one is due.
            if now - last_recv > DEAD_AFTER:
                return "disconnected"  # silent link: no traffic despite pings
            if now - last_ping >= PING_INTERVAL:
                ws.ping()
                last_ping = now

            if stdin_fd in readable:
                try:
                    data = os.read(stdin_fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    return "ended"  # local EOF (e.g. Ctrl-D at a closed stdin)
                forward, actions = escapes.feed(data)
                # Forward typed-through bytes BEFORE acting on any escape, so
                # e.g. "echo hi\n~." still delivers the line before we quit.
                if forward:
                    ws.send_text(json.dumps({"type": "input",
                                             "data": forward.decode("utf-8", "surrogateescape")}))
                for action in actions:
                    if action == "quit":
                        log("exe-ssh: closed (escape ~.).")
                        return "ended"
                    elif action == "help":
                        sys.stderr.write(ESCAPE_HELP)
                        sys.stderr.flush()
                    elif action == "suspend":
                        suspend(stdin_fd, old_termios)
                        # Resuming: the foreground size may have changed and a
                        # ping is overdue, so refresh both.
                        winch["flag"] = True
                        last_recv = last_ping = time.monotonic()
    finally:
        signal.signal(signal.SIGWINCH, old_winch)


def suspend(stdin_fd, old_termios):
    """Suspend exe-ssh (``~^Z``), like ssh(1). Restore cooked-mode terminal,
    stop ourselves with SIGTSTP so the user's shell takes over, then re-enter
    raw mode when we're foregrounded again with ``fg``."""
    log("exe-ssh: suspended; resume with 'fg'.")
    termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_termios)
    # Force the default SIGTSTP disposition so the kernel actually stops us,
    # in case anything installed a handler. SIGCONT already resumes us by
    # default, so leave it alone.
    old_tstp = signal.signal(signal.SIGTSTP, signal.SIG_DFL)
    try:
        os.kill(os.getpid(), signal.SIGTSTP)
    finally:
        # Back in the foreground: restore the handler and raw mode. Use
        # TCSADRAIN (not setraw's default TCSAFLUSH) so type-ahead survives.
        signal.signal(signal.SIGTSTP, old_tstp)
        tty.setraw(stdin_fd, termios.TCSADRAIN)


def handle_server_message(payload):
    try:
        msg = json.loads(payload)
    except ValueError:
        return
    if msg.get("type") == "output":
        raw = base64.b64decode(msg.get("data", ""))
        os.write(sys.stdout.fileno(), raw)


def main():
    parser = argparse.ArgumentParser(
        prog="experimental-exe-ssh.py",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        description="An SSH-ish interactive terminal for an exe.dev VM, over "
                    "HTTPS (no port 22). The session is persistent and the "
                    "client auto-reconnects.",
        epilog="escape sequences (typed right after Enter, like ssh):\n"
               "  ~.   terminate the connection\n"
               "  ~^Z  suspend exe-ssh (resume with 'fg')\n"
               "  ~~   send a literal '~'\n"
               "  ~?   list these sequences")
    parser.add_argument("vm", help="VM name (e.g. willow-wind), or a "
                                   "fully-qualified box host (e.g. "
                                   "willow-wind.exe.xyz)")
    parser.add_argument("session", nargs="?", default=None,
                        help="named terminal session to attach to "
                             "(default: a random color name)")
    parser.add_argument("-l", "--list", action="store_true",
                        help="list this VM's existing terminal sessions and exit")
    parser.add_argument("-i", "--key", metavar="PATH", default=None,
                        help="SSH private key to sign with "
                             "(default: ask ssh -G)")
    # Hidden knobs for testing against a local non-TLS stack on a random port.
    # Not for production use; deliberately undocumented.
    parser.add_argument("--port", type=int, default=443, help=argparse.SUPPRESS)
    parser.add_argument("--plaintext", action="store_true",
                        help=argparse.SUPPRESS)
    parser.add_argument("--connect-host", default=None, help=argparse.SUPPRESS)
    args = parser.parse_args()

    # The VM arg is either a bare name (-> <name>.xterm.exe.xyz) or a
    # fully-qualified box host like <name>.exe.xyz (-> <name>.xterm.exe.xyz).
    name, _, base = args.vm.partition(".")
    base = base or "exe.xyz"
    # Validate before interpolating into HTTP headers and namespaces, so they
    # can't smuggle CRLF or other junk.
    for label, val in (("vm", name), ("host", base)):
        if not val or any(c in val for c in "\r\n /\\:@?#") or val != val.strip():
            sys.exit(f"error: invalid {label}: {val!r}")

    # The terminal endpoint is scoped to its own namespace (v0@<vm>.xterm.<base>),
    # distinct from the general VM-proxy namespace (v0@<vm>.<base>): the token
    # is bound to exactly this VM's terminal.
    host = f"{name}.xterm.{base}"
    namespace = f"v0@{host}"
    key_path = find_key(args.key, host)
    insecure = os.environ.get("EXE_SSH_INSECURE") == "1"

    def auth_headers():
        # Re-sign a fresh short-TTL token per request/connect.
        return {"Authorization": f"Bearer {generate_token(key_path, namespace)}"}

    def fetch_sessions():
        resp = http_get_json(host, args.port, "/terminal/sessions", auth_headers(),
                             insecure, plaintext=args.plaintext,
                             connect_host=args.connect_host)
        sessions = resp.get("sessions") if isinstance(resp, dict) else None
        if not isinstance(sessions, list):
            raise WSError("unexpected sessions response")
        names = []
        for s in sessions:
            name = s.get("name") if isinstance(s, dict) else None
            if isinstance(name, str) and name:
                names.append(name)
        return names

    # --list: print the VM's existing sessions and exit. This is a plain GET,
    # so it works without a tty (e.g. piped into a script).
    if args.list:
        try:
            names = fetch_sessions()
        except (OSError, WSError, ssl.SSLError, ValueError) as e:
            sys.exit(f"error: could not list sessions: {e}")
        if names:
            for n in names:
                print(n)
        else:
            log(f"exe-ssh: no terminal sessions on {args.vm}.")
        return

    # Attaching needs an interactive terminal; check before doing any network
    # work (e.g. the best-effort session list below).
    if not sys.stdin.isatty():
        sys.exit("error: stdin is not a tty; this client needs an interactive terminal")

    # Pick the session: explicit arg, or a random color name (avoiding names
    # already in use when we can enumerate them).
    session = args.session
    if session is None:
        existing = []
        try:
            existing = fetch_sessions()
        except (OSError, WSError, ssl.SSLError, ValueError):
            pass  # best-effort; fall back to a plain random name
        session = random_session_name(avoid=existing)
        log(f"exe-ssh: new session '{session}' (pass a name to reattach; -l to list)")
    if not re.fullmatch(r"[a-zA-Z0-9-]+", session):
        sys.exit(f"error: invalid session name: {session!r} "
                 "(letters, digits and dashes only)")

    qs = urlencode({"name": session})
    path = f"/terminal/ws/experimental-exe-ssh?{qs}"

    log(f"exe-ssh: connecting to {args.vm} (session '{session}') over https...")

    stdin_fd = sys.stdin.fileno()
    old_termios = termios.tcgetattr(stdin_fd)

    def restore_and_exit(signum, frame):
        # Restore the local terminal before dying on SIGTERM/SIGHUP so we
        # don't leave the user's shell stuck in raw mode.
        termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_termios)
        sys.stderr.write("\r\n")
        os._exit(143)

    for sig in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, restore_and_exit)

    backoff = 0.5
    first = True
    try:
        tty.setraw(stdin_fd)
        while True:
            # Re-sign a fresh token per (re)connect so long sessions don't
            # outlive their token's short TTL.
            headers = auth_headers()
            try:
                ws = WebSocket.connect(host, args.port, path, headers,
                                       insecure, plaintext=args.plaintext,
                                       connect_host=args.connect_host)
            except (OSError, WSError, ssl.SSLError) as e:
                if first:
                    # Restore the terminal before reporting a hard failure so
                    # the message isn't mangled by raw mode.
                    termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_termios)
                    sys.exit(f"error: connect failed: {e}")
                log(f"exe-ssh: reconnect failed ({e}); retrying in {backoff:.1f}s")
                time.sleep(backoff)
                backoff = min(backoff * 2, 10)
                continue

            backoff = 0.5
            if not first:
                banner("\u2713 reconnected", color=GREEN)
            first = False
            reason = "disconnected"
            try:
                reason = run_session(ws, stdin_fd, old_termios)
            except (OSError, WSError, ssl.SSLError) as e:
                # Mid-session network failure: fall through to reconnect
                # rather than dumping a traceback. This is the common case
                # the auto-reconnect feature exists for.
                log(f"exe-ssh: connection lost ({e})")
            finally:
                ws.close()

            if reason == "ended":
                # The remote shell exited (Ctrl-D / logout) or the user asked
                # to quit (~.). Reconnecting would just spawn a fresh shell, so
                # quit instead -- this matches what ssh(1) does.
                log("exe-ssh: session ended.")
                break

            banner("\u26a1 connection lost \u2014 reconnecting\u2026 (Ctrl-C to quit)",
                   color=YELLOW)
            time.sleep(0.3)
    except KeyboardInterrupt:
        pass
    finally:
        termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_termios)
        sys.stderr.write("\r\n")


if __name__ == "__main__":
    main()
