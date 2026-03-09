"""Missive data source: HTTP client + domain objects.

Client layer (host-side, never exposed to sandbox):
    MissiveClient — urllib.request-based Missive REST API client with
    429 retry. Uses Bearer auth.

Proxy objects (exposed to sandbox via allowlist):
    MissiveSource       — root entry point: conversations, shared_labels,
                          contacts, fetch_conversations(), search_contacts()
    MissiveConversation — single conversation with lazy messages + comments
    MissiveMessage      — single email/SMS/etc. message with metadata
    MissiveComment      — single internal team comment
    MissiveContact      — single contact

Missive's API paginates via cursor-based timestamps (pass `until=<oldest
last_activity_at>`), not offsets. Page sizes are small (max 50 for
conversations, max 10 for messages/comments). See proxy_api_design.md
for the design philosophy and sources/missive.md for API docs.
"""

import html
import html.parser
import json
import logging
import re
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from urllib.parse import urlencode

from panopticon.proxy import ProxyObject

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Client layer (host-side only)
# ---------------------------------------------------------------------------

_API_BASE = "https://public.missiveapp.com/v1"


class MissiveClient:
    """Missive REST API client using urllib.request.

    Handles 429 retry. Bearer auth.
    Never exposed to the sandbox — stored in _client attrs on domain objects.
    """

    def __init__(self, token: str):
        token = (token or "").strip()
        if not token:
            raise ValueError("EXE_MISSIVE_API_KEY must be set")
        self._token = token

    def _request(self, path: str, params: dict | None = None) -> dict | list:
        """Make an authenticated GET request. Returns parsed JSON."""
        url = f"{_API_BASE}{path}"
        if params:
            filtered = {k: str(v) for k, v in params.items() if v is not None}
            if filtered:
                url = f"{url}?{urlencode(filtered)}"

        req = urllib.request.Request(
            url,
            headers={
                "Authorization": f"Bearer {self._token}",
                "Accept": "application/json",
            },
        )

        for attempt in range(2):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    return json.loads(resp.read())
            except urllib.error.HTTPError as exc:
                if exc.code == 429 and attempt == 0:
                    retry_after = int(exc.headers.get("Retry-After", "5"))
                    log.warning("Missive 429 — retrying after %ds", retry_after)
                    time.sleep(retry_after)
                    continue
                raise

        raise RuntimeError("unreachable")  # pragma: no cover

    def list_conversations(
        self,
        limit: int = 50,
        until: str | None = None,
        inbox: bool | None = None,
        closed: bool | None = None,
        team_inbox: str | None = None,
        team_closed: str | None = None,
        email: str | None = None,
        domain: str | None = None,
        shared_label: str | None = None,
    ) -> list[dict]:
        """List conversations visible to the token owner.

        Cursor-based pagination: pass until=<last_activity_at of oldest result>
        for the next page. May return more than `limit` items.
        """
        params: dict = {"limit": str(min(limit, 50))}
        if until is not None:
            params["until"] = until
        # Missive requires at least one mailbox filter; default to "all".
        has_mailbox = any(v is not None for v in (inbox, closed, team_inbox, team_closed))
        if not has_mailbox:
            params["all"] = "true"
        if inbox is not None:
            params["inbox"] = str(inbox).lower()
        if closed is not None:
            params["closed"] = str(closed).lower()
        if team_inbox is not None:
            params["team_inbox"] = team_inbox
        if team_closed is not None:
            params["team_closed"] = team_closed
        if email is not None:
            params["email"] = email
        if domain is not None:
            params["domain"] = domain
        if shared_label is not None:
            params["shared_label"] = shared_label
        data = self._request("/conversations", params)
        return data.get("conversations", []) if isinstance(data, dict) else []

    def list_messages(
        self,
        conversation_id: str,
        limit: int = 10,
        until: str | None = None,
    ) -> list[dict]:
        """List messages in a conversation with full bodies (max 10 per page).

        The conversation messages endpoint returns metadata only (no body).
        We batch-fetch full messages via GET /messages/:id1,:id2 to fill in
        the body field.
        """
        params: dict = {"limit": str(min(limit, 10))}
        if until is not None:
            params["until"] = until
        data = self._request(f"/conversations/{conversation_id}/messages", params)
        msgs = data.get("messages", []) if isinstance(data, dict) else []
        msgs = [m for m in msgs if isinstance(m, dict)]
        if not msgs:
            return msgs
        # Batch-fetch full messages (with body) by comma-separated IDs.
        # Missive returns different shapes:
        #   single ID  → {"messages": {…single object…}}
        #   multi  IDs → {"messages": [{…}, {…}]}
        ids = ",".join(m["id"] for m in msgs if m.get("id"))
        if ids:
            full = self._request(f"/messages/{ids}")
            full_msgs = full.get("messages", []) if isinstance(full, dict) else []
            if isinstance(full_msgs, dict):
                full_msgs = [full_msgs]
            body_map = {m["id"]: m.get("body", "") for m in full_msgs if isinstance(m, dict) and m.get("id")}
            if len(body_map) < len(msgs):
                log.warning(
                    "Missive batch-fetch returned %d/%d messages",
                    len(body_map), len(msgs),
                )
            preview_fallbacks = 0
            empty_count = 0
            for m in msgs:
                body = body_map.get(m.get("id", ""), "")
                if not body:
                    preview = m.get("preview", "")
                    if preview:
                        body = preview
                        preview_fallbacks += 1
                    else:
                        empty_count += 1
                m["body"] = body
            if preview_fallbacks:
                log.warning(
                    "Missive: %d/%d messages had no body — fell back to preview",
                    preview_fallbacks, len(msgs),
                )
            if empty_count:
                log.warning(
                    "Missive: %d/%d messages have no body or preview (data loss)",
                    empty_count, len(msgs),
                )
        return msgs

    def list_comments(
        self,
        conversation_id: str,
        limit: int = 10,
    ) -> list[dict]:
        """List internal comments on a conversation (max 10 per page)."""
        params: dict = {"limit": str(min(limit, 10))}
        data = self._request(f"/conversations/{conversation_id}/comments", params)
        return data.get("comments", []) if isinstance(data, dict) else []

    def list_contacts(
        self,
        search: str | None = None,
        contact_book: str | None = None,
        modified_since: str | None = None,
    ) -> list[dict]:
        """List contacts. Supports search (full-text across all fields)."""
        params: dict = {}
        if search is not None:
            params["search"] = search
        if contact_book is not None:
            params["contact_book"] = contact_book
        if modified_since is not None:
            params["modified_since"] = modified_since
        data = self._request("/contacts", params)
        return data.get("contacts", []) if isinstance(data, dict) else []

    def list_shared_labels(self) -> list[dict]:
        """List all shared labels for the organization."""
        data = self._request("/shared_labels")
        return data.get("shared_labels", []) if isinstance(data, dict) else []


# ---------------------------------------------------------------------------
# HTML-to-text helper
# ---------------------------------------------------------------------------


class _HTMLStripper(html.parser.HTMLParser):
    """Minimal HTML tag stripper using the stdlib parser."""

    _BLOCK_TAGS = frozenset({
        "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6",
        "blockquote", "hr",
    })
    _OPAQUE_TAGS = frozenset({"style", "script"})

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self._parts: list[str] = []
        self._suppress = 0  # depth counter for opaque tags

    def handle_starttag(self, tag, attrs):
        if tag in self._OPAQUE_TAGS:
            self._suppress += 1
        elif tag in self._BLOCK_TAGS:
            self._parts.append("\n")

    def handle_endtag(self, tag):
        if tag in self._OPAQUE_TAGS:
            self._suppress = max(0, self._suppress - 1)
        elif tag in self._BLOCK_TAGS:
            self._parts.append("\n")

    def handle_data(self, data):
        if not self._suppress:
            self._parts.append(data)


def _sanitize_address(raw: dict) -> dict:
    """Normalize a Missive address dict, replacing None with sentinels.

    Missive returns null for missing display names (bare email addresses).
    A sentinel avoids ambiguity and Pyodide's jsnull rendering.
    """
    if not raw:
        return {"address": "[unknown sender]"}
    out = {}
    for k, v in raw.items():
        if v is None:
            out[k] = "[no name]" if k == "name" else ""
        else:
            out[k] = v
    return out


def _unix_to_iso(ts) -> str:
    """Convert a Missive Unix timestamp to ISO 8601, or return '' for missing."""
    if isinstance(ts, str):
        try:
            ts = float(ts)
        except (ValueError, OverflowError):
            return ""
    if isinstance(ts, (int, float)) and ts > 0:
        return datetime.fromtimestamp(ts, tz=timezone.utc).isoformat()
    return ""


def _to_missive_cursor(ts) -> str | None:
    """Convert an ISO 8601 timestamp (or numeric string) to a Missive API cursor.

    Missive pagination cursors are Unix timestamps. The proxy layer exposes
    ISO strings, so this converts back. Passes through numeric values as-is.
    Returns None for None.
    """
    if ts is None:
        return None
    s = str(ts)
    # Already numeric? Pass through.
    try:
        float(s)
        return s
    except ValueError:
        pass
    # ISO 8601 → Unix timestamp string
    dt = datetime.fromisoformat(s.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return str(dt.timestamp())


def _html_to_text(body: str) -> str:
    """Convert HTML email body to plain text.

    Strips tags, converts block elements to newlines, collapses whitespace.
    Returns the input unchanged if it doesn't look like HTML.
    """
    if not body or "<" not in body:
        return body or ""
    stripper = _HTMLStripper()
    stripper.feed(body)
    text = "".join(stripper._parts)
    # Collapse runs of whitespace/newlines into single newlines or spaces.
    text = re.sub(r"\n[ \t]*\n+", "\n\n", text)
    text = re.sub(r"[ \t]+", " ", text)
    return text.strip()


# ---------------------------------------------------------------------------
# Proxy objects (sandbox-visible)
# ---------------------------------------------------------------------------


class MissiveComment(ProxyObject):
    """A single Missive internal comment (team-only, invisible to customer)."""

    def __init__(self, raw: dict):
        author = raw.get("author", {})
        author_name = author.get("name", "") or author.get("email", "unknown")
        created = _unix_to_iso(raw.get("created_at"))

        super().__init__(
            proxy_id=f"missive_comment_{raw.get('id', '')}",
            type_name="MissiveComment",
            doc=f"Internal comment by {author_name} ({created}).\n"
                "Team-only annotation invisible to the customer. Access .body for text.",
            dir_attrs=["id", "author", "body", "created_at"],
            attr_docs={
                "id": "Comment ID (string)",
                "author": "Dict with 'name' and/or 'email' of the commenter",
                "body": "Comment text (plain text, HTML tags stripped)",
                "created_at": "ISO 8601 timestamp of creation",
            },
        )
        self.id = raw.get("id", "")
        self.author = {"name": author.get("name", ""), "email": author.get("email", "")}
        self.body = _html_to_text(raw.get("body", ""))
        self.created_at = created


class MissiveMessage(ProxyObject):
    """A single Missive message (email, SMS, WhatsApp, etc.)."""

    def __init__(self, raw: dict):
        subject = raw.get("subject", "") or ""
        preview = raw.get("preview", "") or ""
        delivered = _unix_to_iso(raw.get("delivered_at"))

        super().__init__(
            proxy_id=f"missive_msg_{raw.get('id', '')}",
            type_name="MissiveMessage",
            doc=f"Message: {subject or preview[:60]}\n"
                f"Delivered: {delivered}\n"
                "Access .body for full content, .from_field/.to_fields for addressing.",
            dir_attrs=["id", "subject", "preview", "from_field", "to_fields",
                        "body", "delivered_at"],
            attr_docs={
                "id": "Message ID (string)",
                "subject": "Email subject line (may be empty for non-email channels)",
                "preview": "Short text preview of the message",
                "from_field": "Sender as dict: {'address': str, 'name': str} for email, "
                    "or {'phone': str} for SMS/WhatsApp",
                "to_fields": "List of recipient dicts, same format as from_field",
                "body": "Full message body (plain text, HTML tags stripped)",
                "delivered_at": "ISO 8601 timestamp of delivery",
            },
        )
        self.id = raw.get("id", "")
        self.subject = subject
        self.preview = preview
        self.from_field = _sanitize_address(raw.get("from_field", {}) or {})
        self.to_fields = [
            _sanitize_address(r)
            for r in (raw.get("to_fields", []) or [])
            if isinstance(r, dict)
        ]
        self.body = _html_to_text(raw.get("body", ""))
        self.delivered_at = delivered


class MissiveContact(ProxyObject):
    """A single Missive contact."""

    def __init__(self, raw: dict):
        name = raw.get("name", "") or ""
        email = ""
        # Contacts may have email_addresses as a list of objects
        emails = raw.get("email_addresses", [])
        if emails and isinstance(emails, list):
            email = emails[0].get("address", "") if isinstance(emails[0], dict) else str(emails[0])
        phone = ""
        phones = raw.get("phone_numbers", [])
        if phones and isinstance(phones, list):
            phone = phones[0].get("number", "") if isinstance(phones[0], dict) else str(phones[0])

        super().__init__(
            proxy_id=f"missive_contact_{raw.get('id', '')}",
            type_name="MissiveContact",
            doc=f"Contact: {name or email or 'unnamed'}",
            dir_attrs=["id", "name", "email", "phone"],
            attr_docs={
                "id": "Contact ID (string)",
                "name": "Contact display name",
                "email": "Primary email address (first from email_addresses list)",
                "phone": "Primary phone number (first from phone_numbers list)",
            },
        )
        self.id = raw.get("id", "")
        self.name = name
        self.email = email
        self.phone = phone


class MissiveConversation(ProxyObject):
    """A single Missive conversation (email thread, SMS thread, etc.)."""

    def __init__(self, client: "MissiveClient", raw: dict):
        conv_id = raw.get("id", "")
        subject = raw.get("subject", "") or raw.get("latest_message_subject", "") or ""
        created = _unix_to_iso(raw.get("created_at"))
        last_activity = _unix_to_iso(raw.get("last_activity_at"))

        # Extract assignees
        assignees = []
        for a in raw.get("assignees", []):
            assignees.append({"name": a.get("name", ""), "email": a.get("email", "")})

        # Keep label dicts (id + name) — consistent with MissiveSource.shared_labels
        labels = [
            {"id": lbl.get("id", ""), "name": lbl.get("name", "")}
            for lbl in raw.get("shared_labels", [])
            if isinstance(lbl, dict)
        ]

        # Team info
        team = raw.get("team", {}) or {}
        team_name = team.get("name", "") if isinstance(team, dict) else ""

        super().__init__(
            proxy_id=f"missive_conv_{conv_id}",
            type_name="MissiveConversation",
            doc=f"Conversation: {subject or '(no subject)'}\n"
                f"Last activity: {last_activity}\n"
                f"Assignees: {', '.join(a['name'] or a['email'] for a in assignees) or 'none'}\n"
                "Access .messages for email/SMS messages, .comments for internal team notes.",
            dir_attrs=["id", "subject", "created_at", "last_activity_at",
                        "assignees", "shared_labels", "team",
                        "messages", "fetch_messages", "comments"],
            attr_docs={
                "id": "Conversation ID (string)",
                "subject": "Conversation subject (from latest message subject if not set directly)",
                "created_at": "ISO 8601 timestamp of creation",
                "last_activity_at": "ISO 8601 timestamp of most recent activity (includes internal "
                    "actions like label changes — may be much newer than the latest message). "
                    "Check msg.delivered_at to verify actual message recency.",
                "assignees": "List of assignee dicts, each with 'name' and 'email'",
                "shared_labels": "List of shared label dicts with 'id' and 'name', "
                    "same format as MissiveSource.shared_labels",
                "team": "Team name this conversation belongs to (empty string if none)",
                "messages": "Messages in this conversation (up to 10, Missive's max per page). "
                    "Lazy-loaded, 1h TTL.",
                "fetch_messages": "fetch_messages(until=None) — Fetch older messages using "
                    "cursor-based pagination. Pass until=<delivered_at of oldest message> "
                    "to get the next page. Returns up to 10 messages per call. "
                    "Hits the API on every call (not cached).",
                "comments": "Internal team comments on this conversation (up to 10). "
                    "Lazy-loaded, 1h TTL.",
            },
            methods={"fetch_messages": self._fetch_messages},
        )
        self._client = client
        self._conv_id = conv_id
        self.id = conv_id
        self.subject = subject
        self.created_at = created
        self.last_activity_at = last_activity
        self.assignees = assignees
        self.shared_labels = labels
        self.team = team_name
        self._messages = None
        self._messages_fetched_at = 0.0
        self._comments = None
        self._comments_fetched_at = 0.0

    def _fetch_messages(self, until=None):
        """Fetch messages with cursor-based pagination. Host-side implementation."""
        api_until = _to_missive_cursor(until)
        raw = self._client.list_messages(self._conv_id, until=api_until)
        return [MissiveMessage(m) for m in raw]

    @property
    def messages(self):
        """Messages in this conversation, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._messages is None or (now - self._messages_fetched_at) > 3600:
            raw = self._client.list_messages(self._conv_id)
            self._messages = [MissiveMessage(m) for m in raw]
            self._messages_fetched_at = now
        return self._messages

    @property
    def comments(self):
        """Internal comments, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._comments is None or (now - self._comments_fetched_at) > 3600:
            raw = self._client.list_comments(self._conv_id)
            self._comments = [MissiveComment(c) for c in raw]
            self._comments_fetched_at = now
        return self._comments


class MissiveSource(ProxyObject):
    """Root entry point for Missive (shared support email queue).

    Navigate: .conversations -> pick a conversation -> .messages / .comments
    Use .fetch_conversations(...) for filtered/paginated queries.
    Use .search_contacts(q) to find contacts by name, email, or phone.
    """

    def __init__(self, client: "MissiveClient"):
        super().__init__(
            proxy_id="missive",
            type_name="MissiveSource",
            doc="Missive shared email/support queue.\n\n"
                "Navigate: .conversations -> pick a conversation -> .messages / .comments\n"
                ".shared_labels shows org-wide label tags.\n"
                ".contacts shows contacts from the default contact book.\n\n"
                ".fetch_conversations(...) for filtered/paginated queries.\n"
                ".search_contacts(q) to find contacts by name, email, or phone.\n\n"
                "Missive's API has no full-text conversation search. Filter in Python:\n"
                "  recent = [c for c in missive.conversations if c.last_activity_at > '2026-03-04']\n"
                "Note: last_activity_at includes internal actions (label changes, assignments),\n"
                "so always verify actual message recency via msg.delivered_at.",
            dir_attrs=["conversations", "shared_labels", "contacts",
                        "fetch_conversations", "search_contacts"],
            attr_docs={
                "conversations": "Recent conversations (up to 50, one page). "
                    "Each has: subject, assignees, shared_labels, messages, comments. "
                    "Lazy-loaded, 1h TTL.",
                "shared_labels": "Org-wide label tags as list of dicts with 'id' and 'name'. "
                    "Lazy-loaded, 1h TTL.",
                "contacts": "Contacts from the default contact book (one page). "
                    "Each has: name, email, phone. Lazy-loaded, 1h TTL.",
                "fetch_conversations": "fetch_conversations(until=None, inbox=None, closed=None, "
                    "team_inbox=None, team_closed=None, email=None, domain=None, "
                    "shared_label=None) — Filtered/paginated conversation listing. "
                    "Pass until=<last_activity_at of oldest result> to get the next page. "
                    "Hits the API on every call (not cached). Store the result in a "
                    "variable if you need it more than once.",
                "search_contacts": "search_contacts(q) — Full-text search across all "
                    "contact fields (name, email, phone, etc.). "
                    "Hits the API on every call (not cached). Store the result in a "
                    "variable if you need it more than once.",
            },
            methods={
                "fetch_conversations": self._fetch_conversations,
                "search_contacts": self._search_contacts,
            },
        )
        self._client = client
        self._conversations = None
        self._conversations_fetched_at = 0.0
        self._shared_labels = None
        self._shared_labels_fetched_at = 0.0
        self._contacts = None
        self._contacts_fetched_at = 0.0

    @property
    def conversations(self):
        """Recent conversations (up to 50), cached with 1-hour TTL."""
        now = time.monotonic()
        if self._conversations is None or (now - self._conversations_fetched_at) > 3600:
            raw = self._client.list_conversations()
            self._conversations = [MissiveConversation(self._client, c) for c in raw]
            self._conversations_fetched_at = now
        return self._conversations

    @property
    def shared_labels(self):
        """Org-wide shared labels, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._shared_labels is None or (now - self._shared_labels_fetched_at) > 3600:
            raw = self._client.list_shared_labels()
            self._shared_labels = [
                {"id": lbl.get("id", ""), "name": lbl.get("name", "")}
                for lbl in raw
            ]
            self._shared_labels_fetched_at = now
        return self._shared_labels

    @property
    def contacts(self):
        """Contacts from the default contact book, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._contacts is None or (now - self._contacts_fetched_at) > 3600:
            raw = self._client.list_contacts()
            self._contacts = [MissiveContact(c) for c in raw]
            self._contacts_fetched_at = now
        return self._contacts

    def _fetch_conversations(
        self, until=None, inbox=None, closed=None, team_inbox=None,
        team_closed=None, email=None, domain=None, shared_label=None,
    ):
        """Host-side fetch_conversations implementation. Called via resolve_call."""
        raw = self._client.list_conversations(
            until=_to_missive_cursor(until),
            inbox=inbox, closed=closed,
            team_inbox=str(team_inbox) if team_inbox is not None else None,
            team_closed=str(team_closed) if team_closed is not None else None,
            email=str(email) if email is not None else None,
            domain=str(domain) if domain is not None else None,
            shared_label=str(shared_label) if shared_label is not None else None,
        )
        return [MissiveConversation(self._client, c) for c in raw]

    def _search_contacts(self, q):
        """Host-side search_contacts implementation. Called via resolve_call."""
        raw = self._client.list_contacts(search=str(q))
        return [MissiveContact(c) for c in raw]
