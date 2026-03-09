"""Multiplexing proxy server: HTTP over Unix Domain Socket.

Routes proxy attribute resolution requests from the Deno/Pyodide sandbox
to the ProxyRegistry. Designed as the single hole poked in the sandbox:
one Unix socket carrying structured HTTP requests.
"""

import json
import os
import socket
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

from panopticon.proxy import ProxyRegistry

# A proxy getattr request is {"proxy_id": "...", "attr": "..."}.
# 8KB is far more than any legitimate request needs.
MAX_BODY_SIZE = 8192


class TokenBucket:
    """Token bucket rate limiter.

    Allows a burst of `capacity` requests, refilling at `rate` tokens/sec.
    """

    def __init__(self, capacity: int = 5000, rate: float = 100.0):
        self._capacity = capacity
        self._rate = rate
        self._tokens = float(capacity)
        self._last = time.monotonic()
        self._lock = threading.Lock()

    def consume(self) -> bool:
        """Try to consume one token. Returns True if consumed, False if empty."""
        with self._lock:
            now = time.monotonic()
            elapsed = now - self._last
            self._tokens = min(self._capacity, self._tokens + elapsed * self._rate)
            self._last = now
            if self._tokens >= 1.0:
                self._tokens -= 1.0
                return True
            return False


class UnixHTTPServer(HTTPServer):
    """HTTPServer that listens on a Unix Domain Socket instead of TCP."""

    address_family = socket.AF_UNIX

    def server_close(self):
        super().server_close()
        try:
            os.unlink(self.server_address)
        except OSError:
            pass


class MuxHandler(BaseHTTPRequestHandler):
    """Handles proxy resolution requests over HTTP."""

    def do_POST(self):
        if self.path not in ("/proxy/getattr", "/proxy/call"):
            self._send_json(404, {"error": f"Unknown path: {self.path}"})
            return

        try:
            content_length = int(self.headers.get("Content-Length", 0))
            if content_length > MAX_BODY_SIZE:
                self._send_json(400, {"error": f"Request body too large: {content_length} > {MAX_BODY_SIZE}"})
                return
            body = self.rfile.read(content_length)
            request = json.loads(body)
            proxy_id = request["proxy_id"]
            if self.path == "/proxy/getattr":
                attr = request["attr"]
            else:
                method = request["method"]
                args = request.get("args", [])
                kwargs = request.get("kwargs", {})
        except (json.JSONDecodeError, KeyError, ValueError) as e:
            self._send_json(400, {"error": f"Bad request: {e}"})
            return

        if not self.server.rate_limiter.consume():
            self._send_json(429, {"error": "Rate limited"})
            return

        try:
            if self.path == "/proxy/getattr":
                result = self.server.registry.resolve_getattr(proxy_id, attr)
            else:
                result = self.server.registry.resolve_call(
                    proxy_id, method, args, kwargs,
                )
            self._send_json(200, result)
        except (KeyError, AttributeError) as e:
            self._send_json(404, {"error": str(e)})
        except TypeError as e:
            self._send_json(400, {"error": str(e)})
        except Exception as e:
            self._send_json(500, {"error": f"Internal error: {e}"})

    def _send_json(self, status, data):
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        """Suppress default HTTP logging."""


class MuxServer:
    """HTTP-over-UDS proxy server with context manager support.

    Wraps UnixHTTPServer lifecycle: starts in a daemon thread,
    exposes socket_path, supports context manager protocol.
    """

    def __init__(self, registry: ProxyRegistry, socket_path: str | None = None):
        if socket_path is None:
            socket_path = os.path.join(tempfile.mkdtemp(), "mux.sock")
        self._socket_path = socket_path
        self._registry = registry
        self._server = None
        self._thread = None

    @property
    def socket_path(self) -> str:
        return self._socket_path

    def start(self):
        """Start the server in a daemon thread."""
        try:
            os.unlink(self._socket_path)
        except OSError:
            pass

        self._server = UnixHTTPServer(self._socket_path, MuxHandler)
        self._server.registry = self._registry
        self._server.rate_limiter = TokenBucket()
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def stop(self):
        """Shut down the server and clean up."""
        if self._server:
            self._server.shutdown()
            self._server.server_close()
            self._server = None
        self._thread = None

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *_):
        self.stop()
