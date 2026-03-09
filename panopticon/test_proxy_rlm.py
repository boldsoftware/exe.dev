"""End-to-end integration tests for the proxy object bridge.

Tests that ProxyRef serializes and injects correctly, attribute access
resolves through the UDS bridge, nested proxy navigation works, and
dir()/doc/iteration all function as expected.

Uses a real PythonInterpreter (Deno + Pyodide) with mock ProxyObjects.
No LLM calls needed — this tests the bridge mechanism only.
"""

import http.client
import socket
import sys
import os

# Add project root to path so deps/ is importable
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import json
import pytest

from panopticon.mux import MuxServer
from panopticon.proxy import ProxyObject, ProxyRegistry
from deps.dspy.primitives.python_interpreter import PythonInterpreter


class UnixHTTPConnection(http.client.HTTPConnection):
    """HTTPConnection over a Unix Domain Socket."""

    def __init__(self, socket_path):
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(self._socket_path)


# =============================================================================
# Mock Data Sources
# =============================================================================


class MockMessage(ProxyObject):
    """A chat message."""

    def __init__(self, msg_id: str, content: str, author: str):
        super().__init__(
            proxy_id=f"msg_{msg_id}",
            type_name="Message",
            doc="A chat message with content and author",
            dir_attrs=["content", "author", "timestamp"],
            attr_docs={
                "content": "The message text",
                "author": "Who sent the message",
                "timestamp": "When it was sent",
            },
        )
        self.content = content
        self.author = author
        self.timestamp = "2024-01-15T10:30:00Z"


class MockChannel(ProxyObject):
    """A chat channel containing messages."""

    def __init__(self, name: str, messages: list[MockMessage]):
        super().__init__(
            proxy_id=f"channel_{name}",
            type_name="Channel",
            doc="A chat channel with messages",
            dir_attrs=["name", "messages", "topic"],
            attr_docs={
                "name": "Channel name",
                "messages": "List of messages in the channel",
                "topic": "Channel topic",
            },
        )
        self.name = name
        self.messages = messages
        self.topic = f"Discussion about {name}"


class MockSource(ProxyObject):
    """A data source with channels."""

    def __init__(self, channels: list[MockChannel]):
        super().__init__(
            proxy_id="discord_source",
            type_name="DiscordSource",
            doc="Discord data source with channels and messages",
            dir_attrs=["channels", "server_name"],
            attr_docs={
                "channels": "List of channels in the server",
                "server_name": "Name of the Discord server",
            },
        )
        self.channels = channels
        self.server_name = "Test Server"


# =============================================================================
# Fixtures
# =============================================================================


@pytest.fixture
def mock_data():
    """Create a mock data hierarchy: source → channels → messages."""
    msgs_general = [
        MockMessage("1", "hello world", "alice"),
        MockMessage("2", "goodbye world", "bob"),
    ]
    msgs_random = [
        MockMessage("3", "random thought", "charlie"),
    ]
    channels = [
        MockChannel("general", msgs_general),
        MockChannel("random", msgs_random),
    ]
    source = MockSource(channels)
    return source


@pytest.fixture
def registry(mock_data):
    """Create a registry with the mock source registered."""
    reg = ProxyRegistry()
    reg.register(mock_data)
    return reg


# =============================================================================
# Tests
# =============================================================================


class TestProxyRegistry:
    """Unit tests for ProxyRegistry (no sandbox needed)."""

    def test_register_and_resolve_concrete(self, registry, mock_data):
        result = registry.resolve_getattr("discord_source", "server_name")
        assert result == {"type": "concrete", "value": "Test Server"}

    def test_resolve_doc(self, registry):
        result = registry.resolve_getattr("discord_source", "__doc__")
        assert result["type"] == "concrete"
        assert "Discord data source" in result["value"]

    def test_resolve_dir(self, registry):
        result = registry.resolve_getattr("discord_source", "__dir__")
        assert result["type"] == "concrete"
        assert "channels" in result["value"]
        assert "server_name" in result["value"]

    def test_resolve_proxy_list(self, registry, mock_data):
        result = registry.resolve_getattr("discord_source", "channels")
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 2
        assert result["items"][0]["type"] == "proxy"
        assert result["items"][0]["type_name"] == "Channel"

    def test_resolve_nested_proxy(self, registry, mock_data):
        # First resolve channels
        channels_result = registry.resolve_getattr("discord_source", "channels")
        channel_id = channels_result["items"][0]["proxy_id"]

        # Then resolve messages on the first channel
        msgs_result = registry.resolve_getattr(channel_id, "messages")
        assert msgs_result["type"] == "proxy_list"
        assert len(msgs_result["items"]) == 2

        # Resolve content on first message
        msg_id = msgs_result["items"][0]["proxy_id"]
        content_result = registry.resolve_getattr(msg_id, "content")
        assert content_result == {"type": "concrete", "value": "hello world"}

    def test_unknown_proxy_id(self, registry):
        with pytest.raises(KeyError, match="Unknown proxy ID"):
            registry.resolve_getattr("nonexistent", "attr")


# =============================================================================
# UDS Bridge Fixtures & Tests
# =============================================================================


@pytest.fixture
def mux_server(registry):
    """Start a MuxServer on a temp socket, stop on teardown."""
    server = MuxServer(registry)
    server.start()
    yield server
    server.stop()


@pytest.fixture
def uds_interpreter(mux_server):
    """PythonInterpreter using UDS bridge."""
    interp = PythonInterpreter(uds_path=mux_server.socket_path)
    yield interp
    interp.shutdown()


class TestUDSBridge:
    """Integration tests using the UDS proxy bridge."""

    def test_concrete_attribute(self, uds_interpreter, mock_data):
        """Access a concrete (string) attribute through the UDS bridge."""
        result = uds_interpreter.execute(
            'print(source.server_name)',
            variables={"source": mock_data},
        )
        assert "Test Server" in result

    def test_proxy_repr(self, uds_interpreter, mock_data):
        """ProxyRef has a useful repr."""
        result = uds_interpreter.execute(
            'print(repr(source))',
            variables={"source": mock_data},
        )
        assert "DiscordSource" in result

    def test_doc_and_dir(self, uds_interpreter, mock_data):
        """Access __doc__ and dir() through the UDS bridge."""
        result = uds_interpreter.execute(
            'print(source.__doc__)\nprint(dir(source))',
            variables={"source": mock_data},
        )
        assert "Discord data source" in result
        assert "channels" in result

    def test_nested_navigation(self, uds_interpreter, mock_data):
        """Navigate source -> channels -> messages -> content via UDS."""
        result = uds_interpreter.execute(
            'channels = source.channels\n'
            'msgs = channels[0].messages\n'
            'print(msgs[0].content)',
            variables={"source": mock_data},
        )
        assert "hello world" in result

    def test_iteration(self, uds_interpreter, mock_data):
        """Iterate over a list of proxy objects via UDS."""
        result = uds_interpreter.execute(
            'channels = source.channels\n'
            'for ch in channels:\n'
            '    print(ch.name)',
            variables={"source": mock_data},
        )
        assert "general" in result
        assert "random" in result

    def test_attribute_caching(self, uds_interpreter, mock_data):
        """Second access to same attribute uses cache."""
        result = uds_interpreter.execute(
            'name1 = source.server_name\n'
            'name2 = source.server_name\n'
            'print(name1 == name2)',
            variables={"source": mock_data},
        )
        assert "True" in result

    def test_concrete_in_list(self, uds_interpreter, mock_data):
        """Access concrete attributes across multiple messages via UDS."""
        result = uds_interpreter.execute(
            'channels = source.channels\n'
            'msgs = channels[0].messages\n'
            'authors = [m.author for m in msgs]\n'
            'print(authors)',
            variables={"source": mock_data},
        )
        assert "alice" in result
        assert "bob" in result


# =============================================================================
# Security Tests
# =============================================================================


class TestSecurityBoundaries:
    """Registry-level security: allowlist and type checking (no sandbox)."""

    def test_allowlist_blocks_proxy_id(self, registry):
        with pytest.raises(AttributeError, match="not exposed"):
            registry.resolve_getattr("discord_source", "_proxy_id")

    def test_allowlist_blocks_class(self, registry):
        with pytest.raises(AttributeError, match="not exposed"):
            registry.resolve_getattr("discord_source", "__class__")

    def test_allowlist_blocks_dict(self, registry):
        with pytest.raises(AttributeError, match="not exposed"):
            registry.resolve_getattr("discord_source", "__dict__")

    def test_allowlist_allows_listed_attrs(self, registry):
        result = registry.resolve_getattr("discord_source", "server_name")
        assert result == {"type": "concrete", "value": "Test Server"}

    def test_allowlist_allows_special_doc(self, registry):
        result = registry.resolve_getattr("discord_source", "__doc__")
        assert result["type"] == "concrete"

    def test_allowlist_allows_special_dir(self, registry):
        result = registry.resolve_getattr("discord_source", "__dir__")
        assert result["type"] == "concrete"

    def test_classify_rejects_bound_method(self, registry):
        """Bound methods are not JSON-serializable and should be rejected."""
        reg = ProxyRegistry()
        obj = ProxyObject(
            proxy_id="test_obj",
            type_name="Test",
            dir_attrs=["bad_attr"],
        )
        obj.bad_attr = obj.__init__  # a bound method
        reg.register(obj)
        with pytest.raises(TypeError, match="not serializable"):
            reg.resolve_getattr("test_obj", "bad_attr")

    def test_classify_rejects_class_object(self, registry):
        """Class objects are not JSON-serializable and should be rejected."""
        reg = ProxyRegistry()
        obj = ProxyObject(
            proxy_id="test_obj2",
            type_name="Test",
            dir_attrs=["bad_attr"],
        )
        obj.bad_attr = ProxyObject  # a class object
        reg.register(obj)
        with pytest.raises(TypeError, match="not serializable"):
            reg.resolve_getattr("test_obj2", "bad_attr")


class TestMuxSecurity:
    """HTTP-level security: error handling and rate limiting over UDS."""

    @pytest.fixture
    def mux(self, registry):
        server = MuxServer(registry)
        server.start()
        yield server
        server.stop()

    def _post(self, mux, body: bytes | str, path: str = "/proxy/getattr"):
        if isinstance(body, str):
            body = body.encode("utf-8")
        conn = UnixHTTPConnection(mux.socket_path)
        conn.request("POST", path, body=body, headers={
            "Content-Type": "application/json",
            "Content-Length": str(len(body)),
        })
        resp = conn.getresponse()
        data = json.loads(resp.read())
        status = resp.status
        conn.close()
        return status, data

    def test_malformed_json_returns_400(self, mux):
        status, data = self._post(mux, b"not json")
        assert status == 400

    def test_missing_fields_returns_400(self, mux):
        status, data = self._post(mux, json.dumps({"proxy_id": "x"}))
        assert status == 400

    def test_unknown_proxy_returns_404(self, mux):
        status, data = self._post(mux, json.dumps({"proxy_id": "nope", "attr": "x"}))
        assert status == 404

    def test_blocked_attr_returns_404(self, mux):
        status, data = self._post(mux, json.dumps({
            "proxy_id": "discord_source", "attr": "__class__",
        }))
        assert status == 404

    def test_oversized_body_returns_400(self, mux):
        body = b"x" * 9000  # exceeds MAX_BODY_SIZE (8192)
        status, data = self._post(mux, body)
        assert status == 400
        assert "too large" in data["error"]

    def test_rate_limiting_returns_429(self, mux):
        # Set bucket to tiny capacity so we can exhaust it
        mux._server.rate_limiter._capacity = 2
        mux._server.rate_limiter._tokens = 2.0
        mux._server.rate_limiter._rate = 0.0  # no refill during test

        body = json.dumps({"proxy_id": "discord_source", "attr": "server_name"})
        # First two should succeed
        s1, _ = self._post(mux, body)
        s2, _ = self._post(mux, body)
        assert s1 == 200
        assert s2 == 200
        # Third should be rate-limited
        s3, d3 = self._post(mux, body)
        assert s3 == 429
