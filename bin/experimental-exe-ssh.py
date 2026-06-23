#!/usr/bin/env python3
"""An SSH-ish interactive terminal for an exe.dev VM, over HTTPS (no port 22).

Usage:
    experimental-exe-ssh.py <vm> [session-name]

The session is persistent and the client auto-reconnects, so dropping the
network (or closing your laptop) reattaches you to the same shell.
"""

# Standard library only!
import argparse
import base64
import errno
import hashlib
import json
import os
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


def log(msg):
    sys.stderr.write("\r\x1b[2K" + msg + "\r\n")
    sys.stderr.flush()


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
                connect_host=None, timeout=20):
        # connect_host lets us TCP-connect somewhere (e.g. localhost) while
        # still presenting `host` in the Host header / SNI / Authorization.
        raw = socket.create_connection((connect_host or host, port), timeout=timeout)
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


# ---------------------------------------------------------------------------
# Terminal session
# ---------------------------------------------------------------------------


def term_size(fd):
    try:
        cols, rows = os.get_terminal_size(fd)
        return rows, cols
    except OSError:
        return 24, 80


def run_session(ws, stdin_fd):
    """Pump bytes between the local tty (stdin_fd) and the websocket.

    Returns a reason string describing why the session ended:
      "ended"        -- the remote shell exited (server closed with
                        CLOSE_SHELL_EXITED, e.g. you hit Ctrl-D / typed exit),
                        or local stdin reached EOF. The caller should NOT
                        reconnect.
      "disconnected" -- the connection dropped (TCP EOF or an abnormal close).
                        The caller should reconnect.
    """
    rows, cols = term_size(stdin_fd)
    ws.send_text(json.dumps({"type": "resize", "rows": rows, "cols": cols,
                             "term": os.environ.get("TERM", "xterm-256color")}))

    winch = {"flag": False}

    def on_winch(signum, frame):
        winch["flag"] = True

    old_winch = signal.signal(signal.SIGWINCH, on_winch)
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

            if ws in readable:
                if not ws.recv_into_buffer():
                    return "disconnected"  # TCP EOF: treat as a dropped link
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
                    if opcode in (WebSocket.OP_TEXT, WebSocket.OP_BINARY):
                        handle_server_message(payload)

            if stdin_fd in readable:
                try:
                    data = os.read(stdin_fd, 65536)
                except OSError:
                    data = b""
                if not data:
                    return "ended"  # local EOF (e.g. Ctrl-D at a closed stdin)
                ws.send_text(json.dumps({"type": "input",
                                         "data": data.decode("utf-8", "surrogateescape")}))
    finally:
        signal.signal(signal.SIGWINCH, old_winch)


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
        description="An SSH-ish interactive terminal for an exe.dev VM, over "
                    "HTTPS (no port 22). The session is persistent and the "
                    "client auto-reconnects.")
    parser.add_argument("vm", help="VM name (e.g. willow-wind), or a "
                                   "fully-qualified box host (e.g. "
                                   "willow-wind.exe.xyz)")
    parser.add_argument("session", nargs="?", default="main",
                        help="named terminal session to attach to "
                             "(default: main)")
    parser.add_argument("-i", "--key", metavar="PATH", default=None,
                        help="SSH private key to sign with "
                             "(default: ask ssh -G)")
    # Hidden knobs for the e1e tests, which run against a local non-TLS stack
    # on a random port. Not for production use; deliberately undocumented.
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
    qs = urlencode({"name": args.session})
    path = f"/terminal/ws/experimental-exe-ssh?{qs}"

    if not sys.stdin.isatty():
        sys.exit("error: stdin is not a tty; this client needs an interactive terminal")

    log(f"exe-ssh: connecting to {args.vm} (session '{args.session}') over https...")

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
            token = generate_token(key_path, namespace)
            headers = {"Authorization": f"Bearer {token}"}
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
                log("exe-ssh: reconnected.")
            first = False
            reason = "disconnected"
            try:
                reason = run_session(ws, stdin_fd)
            except (OSError, WSError, ssl.SSLError) as e:
                # Mid-session network failure: fall through to reconnect
                # rather than dumping a traceback. This is the common case
                # the auto-reconnect feature exists for.
                log(f"exe-ssh: connection lost ({e})")
            finally:
                ws.close()

            if reason == "ended":
                # The remote shell exited (Ctrl-D / logout). Reconnecting would
                # just spawn a fresh shell, so quit instead -- this matches
                # what ssh(1) does when the remote shell exits.
                log("exe-ssh: session ended.")
                break

            log("exe-ssh: reconnecting (Ctrl-C to quit)...")
            time.sleep(0.3)
    except KeyboardInterrupt:
        pass
    finally:
        termios.tcsetattr(stdin_fd, termios.TCSADRAIN, old_termios)
        sys.stderr.write("\r\n")


if __name__ == "__main__":
    main()
