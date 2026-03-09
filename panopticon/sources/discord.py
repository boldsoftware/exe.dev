"""Discord data source: HTTP client + domain objects.

Client layer (host-side, never exposed to sandbox):
    DiscordClient — urllib.request-based Discord REST API client with
    snowflake-based time queries and 429 retry.

Proxy objects (exposed to sandbox via allowlist):
    DiscordSource  — root entry point: guild name, channels, threads,
                     snowflake/timestamp helpers
    DiscordChannel — channel or thread with recent messages
    DiscordMessage — single message with metadata

Discord's bot API has no search endpoint, so the agent filters messages
in Python. Snowflake conversion helpers let the agent work in both
timestamp and snowflake space. See proxy_api_design.md for the design
philosophy.
"""

import json
import logging
import time
import urllib.error
import urllib.request
from urllib.parse import urlencode

from panopticon.proxy import ProxyObject

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Client layer (host-side only)
# ---------------------------------------------------------------------------

_API_BASE = "https://discord.com/api/v10"

# Discord epoch for snowflake math (2015-01-01T00:00:00Z)
_DISCORD_EPOCH_MS = 1420070400000


def snowflake_from_timestamp(ts: float) -> int:
    """Convert a Unix timestamp (seconds) to a Discord snowflake ID."""
    ms = int(ts * 1000)
    return (ms - _DISCORD_EPOCH_MS) << 22


def timestamp_from_snowflake(snowflake_id: int) -> float:
    """Convert a Discord snowflake ID to a Unix timestamp (seconds)."""
    ms = (int(snowflake_id) >> 22) + _DISCORD_EPOCH_MS
    return ms / 1000.0


def _coerce_snowflake(value):
    """Coerce a value to a snowflake string, accepting snowflakes, ISO dates, or Unix timestamps."""
    if value is None:
        return None
    s = str(value)
    # Already a snowflake (all-digit string)
    if s.isdigit():
        return s
    # Large float/int that looks like a snowflake (e.g. 1.479e+18 from JS precision loss).
    # Snowflakes are > 1e15; Unix timestamps are < 2e10. No overlap.
    try:
        f = float(s)
        if f > 1e15:
            return str(int(f))
    except (ValueError, OverflowError):
        pass
    # ISO date or datetime string (e.g. '2026-03-06' or '2026-03-06T12:00:00')
    if len(s) >= 10 and s[4:5] == "-":
        from datetime import datetime, timezone
        for fmt in ("%Y-%m-%dT%H:%M:%S", "%Y-%m-%dT%H:%M", "%Y-%m-%d"):
            try:
                dt = datetime.strptime(s.rstrip("Z"), fmt).replace(tzinfo=timezone.utc)
                return str(snowflake_from_timestamp(dt.timestamp()))
            except ValueError:
                continue
    # Unix timestamp (int or float)
    try:
        ts = float(s)
        if ts > 1e9:  # plausible Unix timestamp
            return str(snowflake_from_timestamp(ts))
    except ValueError:
        pass
    raise ValueError(f"Cannot convert {value!r} to a Discord snowflake")


class DiscordClient:
    """Discord REST API client using urllib.request.

    Handles 429 rate limiting with Retry-After.
    Never exposed to the sandbox — stored in _client attrs on domain objects.
    """

    def __init__(self, token: str):
        token = (token or "").strip()
        if not token:
            raise ValueError("EXE_DISCORD_BOT_TOKEN must be set")
        self._token = token

    def _request(self, path: str, params: dict | None = None) -> list | dict:
        """Make an authenticated GET request. Returns parsed JSON."""
        url = f"{_API_BASE}{path}"
        if params:
            filtered = {k: str(v) for k, v in params.items() if v is not None}
            if filtered:
                url = f"{url}?{urlencode(filtered)}"

        req = urllib.request.Request(
            url,
            headers={
                "Authorization": f"Bot {self._token}",
                "User-Agent": "DiscordBot (https://panopticon, 1.0)",
            },
        )

        for attempt in range(2):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    headers = {k.lower(): v for k, v in resp.getheaders()}
                    data = json.loads(resp.read())
                    self._check_rate_limit(headers)
                    return data
            except urllib.error.HTTPError as exc:
                if exc.code == 429 and attempt == 0:
                    body = json.loads(exc.read())
                    retry_after = float(body.get("retry_after", 5))
                    log.warning("Discord 429 — retrying after %.1fs", retry_after)
                    time.sleep(retry_after)
                    continue
                raise

        raise RuntimeError("unreachable")  # pragma: no cover

    def _check_rate_limit(self, headers: dict):
        remaining = headers.get("x-ratelimit-remaining")
        if remaining is not None and int(remaining) < 5:
            reset_after = headers.get("x-ratelimit-reset-after", "?")
            # Log at debug — actual 429s are already retried with a warning.
            # This low-remaining notice is just proactive monitoring; logging
            # at WARNING would surface it to the RLM agent via WarningCollector
            # and produce confusing "rate limit hit" notes in the newsletter.
            log.debug(
                "Discord rate limit low: %s remaining, resets in %ss",
                remaining, reset_after,
            )

    def list_guilds(self) -> list[dict]:
        """List guilds the bot is in."""
        return self._request("/users/@me/guilds")

    def list_channels(self, guild_id: str) -> list[dict]:
        """List all channels in a guild."""
        return self._request(f"/guilds/{guild_id}/channels")

    def list_active_threads(self, guild_id: str) -> list[dict]:
        """List active threads (including forum threads) in a guild."""
        data = self._request(f"/guilds/{guild_id}/threads/active")
        # Discord returns {"threads": [...], "members": [...]}
        return data.get("threads", []) if isinstance(data, dict) else []

    def list_messages(
        self,
        channel_id: str,
        limit: int = 100,
        after: str | None = None,
        before: str | None = None,
        max_pages: int = 2,
    ) -> list[dict]:
        """Fetch recent messages from a channel, paginating backwards.

        Returns messages newest-first (up to limit * max_pages).
        """
        all_msgs: list[dict] = []
        params: dict = {"limit": str(min(limit, 100))}
        if after is not None:
            params["after"] = after
        if before is not None:
            params["before"] = before

        for _ in range(max_pages):
            batch = self._request(f"/channels/{channel_id}/messages", params)
            if not batch:
                break
            all_msgs.extend(batch)
            if len(batch) < int(params["limit"]):
                break
            if after is not None:
                # When after is set, Discord returns oldest-first → paginate forward
                params = {"limit": str(min(limit, 100)), "after": batch[-1]["id"]}
            else:
                # Otherwise newest-first → paginate backward
                params = {"limit": str(min(limit, 100)), "before": batch[-1]["id"]}

        return all_msgs


# ---------------------------------------------------------------------------
# Proxy objects (sandbox-visible)
# ---------------------------------------------------------------------------


def _extract_reactions(raw: dict) -> dict:
    """Extract reaction emoji -> count from Discord's reactions array."""
    result = {}
    for r in raw.get("reactions", []):
        emoji = r.get("emoji", {})
        name = emoji.get("name", "?")
        count = r.get("count", 0)
        if count > 0:
            result[name] = count
    return result


class DiscordMessage(ProxyObject):
    """A single Discord message."""

    def __init__(self, raw: dict, guild_id: str = "", channel_id: str = ""):
        author = raw.get("author", {}).get("username", "unknown")
        timestamp = raw.get("timestamp", "")
        msg_id = raw["id"]

        url = ""
        if guild_id and channel_id:
            url = f"https://discord.com/channels/{guild_id}/{channel_id}/{msg_id}"

        super().__init__(
            proxy_id=f"discord_msg_{msg_id}",
            type_name="DiscordMessage",
            doc=f"Message by {author} at {timestamp}.\n"
                "Access .content for the message text, .reactions for emoji counts.",
            dir_attrs=["id", "author", "content", "timestamp", "channel_id",
                        "url", "reactions", "type", "attachments"],
            attr_docs={
                "id": "Message snowflake ID (string). Use with fetch_messages(after=..., before=...)",
                "author": "Discord username of the message author",
                "content": "Message text content",
                "timestamp": "ISO 8601 timestamp when the message was sent",
                "channel_id": "Channel snowflake ID this message belongs to",
                "url": "Web URL to this message (https://discord.com/channels/...)",
                "reactions": "Dict of emoji name to count, e.g. {'thumbsup': 3}. Only non-zero.",
                "type": "Message type (int). 0=default, 19=reply. Non-zero types include "
                    "system messages (member join, boost, pin, etc.).",
                "attachments": "List of file attachments, each {'filename': str, 'url': str, 'size': int}. "
                    "Empty list if none.",
            },
        )
        self.id = msg_id
        self.author = author
        content = raw.get("content", "")
        if not content and raw.get("attachments"):
            content = "[no text — check .attachments]"
        self.content = content
        self.timestamp = timestamp
        self.channel_id = channel_id
        self.url = url
        self.reactions = _extract_reactions(raw)
        self.type = raw.get("type", 0)
        self.attachments = [
            {"filename": a.get("filename", ""), "url": a.get("url", ""), "size": a.get("size", 0)}
            for a in raw.get("attachments", [])
        ]


class DiscordChannel(ProxyObject):
    """A Discord channel, forum, or thread with recent messages."""

    def __init__(self, client: "DiscordClient", raw: dict, guild_id: str = ""):
        name = raw.get("name", "")

        super().__init__(
            proxy_id=f"discord_chan_{raw['id']}",
            type_name="DiscordChannel",
            doc=f"Discord channel #{name}.\n"
                "Access .messages for recent messages (up to 200, newest first).\n"
                "Use .fetch_messages(after, before, limit) for arbitrary time windows.\n"
                "after/before accept snowflake IDs — use msg.id directly or\n"
                "discord.snowflake_from_timestamp() to convert from timestamps.\n"
                "Filter in Python:\n"
                "  recent = [m for m in ch.messages if m.timestamp > 'YYYY-MM-DD']",
            dir_attrs=["id", "name", "topic", "type", "parent_id",
                        "messages", "fetch_messages"],
            attr_docs={
                "id": "Channel snowflake ID (string)",
                "name": "Channel name (without #)",
                "topic": "Channel topic/description",
                "type": "Channel type (int): 0=text, 5=announcement, 15=forum, "
                    "11=public thread, 12=private thread",
                "parent_id": "Parent channel ID for threads, or category ID "
                    "for top-level channels. Empty string if none.",
                "messages": "Recent messages (up to 200), sorted newest-first. "
                            "Each has: id, author, content, timestamp, channel_id, url, reactions",
                "fetch_messages": "fetch_messages(after=None, before=None, limit=200) — "
                                  "Fetch messages in an arbitrary time window. after/before "
                                  "are message ID snowflakes (use msg.id). Returns up to limit "
                                  "messages (max 1000), newest first. "
                                  "Hits the API on every call (not cached). Store the result "
                                  "in a variable if you need it more than once.",
            },
            methods={"fetch_messages": self._fetch_messages},
        )
        self._client = client
        self._channel_id = raw["id"]
        self._guild_id = guild_id
        self.id = raw["id"]
        self.name = name
        self.topic = raw.get("topic", "") or ""
        self.type = raw.get("type", 0)
        self.parent_id = raw.get("parent_id", "") or ""
        self._messages = None
        self._messages_fetched_at = 0.0

    def _fetch_messages(self, after=None, before=None, limit=200):
        """Fetch messages in an arbitrary time window. Host-side implementation."""
        after = _coerce_snowflake(after)
        before = _coerce_snowflake(before)
        limit = max(1, min(int(limit), 1000))
        try:
            raw = self._client.list_messages(
                self._channel_id, after=after, before=before,
                limit=100, max_pages=(limit + 99) // 100,
            )
        except urllib.error.HTTPError as exc:
            if exc.code == 403:
                log.warning("Discord 403 on channel %s (#%s) — bot lacks read permission", self._channel_id, self.name)
                return []
            raise
        return [DiscordMessage(m, guild_id=self._guild_id, channel_id=self._channel_id) for m in raw[:limit]]

    @property
    def messages(self):
        """Recent messages, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._messages is None or (now - self._messages_fetched_at) > 3600:
            try:
                raw = self._client.list_messages(self._channel_id)
            except urllib.error.HTTPError as exc:
                if exc.code == 403:
                    log.warning("Discord 403 on channel %s (#%s) — bot lacks read permission", self._channel_id, self.name)
                    self._messages = []
                    self._messages_fetched_at = now
                    return self._messages
                raise
            self._messages = [DiscordMessage(m, guild_id=self._guild_id, channel_id=self._channel_id) for m in raw]
            self._messages_fetched_at = now
        return self._messages


class DiscordSource(ProxyObject):
    """Root entry point for a Discord server (guild)."""

    def __init__(self, client: "DiscordClient", guild_id: str, guild_name: str):
        super().__init__(
            proxy_id=f"discord_{guild_id}",
            type_name="DiscordSource",
            doc=f"Discord server '{guild_name}'.\n\n"
                "Navigate: .channels -> pick a channel -> .messages\n"
                "         .active_threads -> pick a thread -> .messages\n"
                "Filter in Python:\n"
                "  recent = [m for m in ch.messages if m.timestamp > 'YYYY-MM-DD']\n"
                "  by_user = [m for m in ch.messages if m.author == 'alice']\n\n"
                "Snowflake helpers:\n"
                "  sf = discord.snowflake_from_timestamp(1700000000.0)  # Unix ts\n"
                "  sf = discord.snowflake_from_timestamp('2025-03-06')  # ISO date\n"
                "  ts = discord.timestamp_from_snowflake(sf)            # -> ISO string\n"
                "  msgs = ch.fetch_messages(after=sf)\n\n"
                "Discord's bot API has no search endpoint. Filter messages in Python.",
            dir_attrs=["guild_name", "channels", "active_threads",
                        "snowflake_from_timestamp", "timestamp_from_snowflake"],
            attr_docs={
                "guild_name": "Name of the Discord server",
                "channels": "Text, announcement, and forum channels (excludes voice/category). "
                            "Each has: name, topic, messages, fetch_messages. "
                            "Forum channels (type 15) are containers for threads; "
                            ".messages returns empty (use .active_threads for forum posts).",
                "active_threads": "Active threads across the server (including forum threads). "
                                  "Separate from .channels — read both for full coverage. "
                                  "Each has: name, topic, messages, fetch_messages.",
                "snowflake_from_timestamp": "snowflake_from_timestamp(ts) — Convert a Unix "
                    "timestamp (float) or ISO date string (e.g. '2025-03-06') to a Discord "
                    "snowflake ID string. Use with ch.fetch_messages(after=..., before=...).",
                "timestamp_from_snowflake": "timestamp_from_snowflake(snowflake_id) — Convert "
                    "a Discord snowflake ID (int or string) to an ISO 8601 timestamp string.",
            },
            methods={
                "snowflake_from_timestamp": self._snowflake_from_timestamp,
                "timestamp_from_snowflake": self._timestamp_from_snowflake,
            },
        )
        self._client = client
        self._guild_id = guild_id
        self.guild_name = guild_name
        self._channels = None
        self._channels_fetched_at = 0.0
        self._active_threads = None
        self._active_threads_fetched_at = 0.0

    def _snowflake_from_timestamp(self, ts):
        """Convert Unix timestamp or ISO date string to snowflake string."""
        if isinstance(ts, str):
            from datetime import datetime, timezone
            dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            ts = dt.timestamp()
        return str(snowflake_from_timestamp(float(ts)))

    def _timestamp_from_snowflake(self, snowflake_id):
        """Convert snowflake to ISO timestamp string."""
        from datetime import datetime, timezone
        unix_ts = timestamp_from_snowflake(int(snowflake_id))
        return datetime.fromtimestamp(unix_ts, tz=timezone.utc).isoformat()

    @property
    def channels(self):
        """Text and announcement channels, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._channels is None or (now - self._channels_fetched_at) > 3600:
            raw = self._client.list_channels(self._guild_id)
            # type 0 = text, type 5 = announcement, type 15 = forum
            self._channels = [
                DiscordChannel(self._client, ch, guild_id=self._guild_id)
                for ch in raw
                if ch.get("type") in (0, 5, 15)
            ]
            self._channels_fetched_at = now
        return self._channels

    @property
    def active_threads(self):
        """Active threads (including forum threads), cached with 1-hour TTL."""
        now = time.monotonic()
        if self._active_threads is None or (now - self._active_threads_fetched_at) > 3600:
            raw = self._client.list_active_threads(self._guild_id)
            self._active_threads = [
                DiscordChannel(self._client, t, guild_id=self._guild_id)
                for t in raw
            ]
            self._active_threads_fetched_at = now
        return self._active_threads
