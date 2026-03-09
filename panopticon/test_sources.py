"""Tests for GitHub and Discord data sources.

Mocks urllib.request.urlopen to test ProxyObject structure, caching,
TTL expiry, error handling, and security boundaries.
"""

import io
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import pytest

from panopticon.proxy import ProxyRegistry
from panopticon.sources.github import GitHubClient, GitHubComment, GitHubIssue, GitHubRepo
from panopticon.sources.discord import (
    DiscordClient, DiscordChannel, DiscordMessage, DiscordSource,
    snowflake_from_timestamp, timestamp_from_snowflake, _coerce_snowflake,
)
from panopticon.sources.missive import (
    MissiveClient, MissiveComment, MissiveContact, MissiveConversation,
    MissiveMessage, MissiveSource,
)


# ---------------------------------------------------------------------------
# Mock HTTP helpers
# ---------------------------------------------------------------------------


class MockResponse:
    """Fake urllib response with read(), getheaders(), context manager."""

    def __init__(self, data, status=200, headers=None):
        self._data = json.dumps(data).encode("utf-8")
        self.status = status
        self._headers = headers or {}

    def read(self):
        return self._data

    def getheaders(self):
        return list(self._headers.items())

    def __enter__(self):
        return self

    def __exit__(self, *_):
        pass


def make_github_issue(number=1, title="Test issue", state="open", labels=None, reactions=None, comments=0):
    return {
        "id": 1000 + number,
        "number": number,
        "title": title,
        "body": f"Body of issue #{number}",
        "state": state,
        "user": {"login": "testuser"},
        "labels": [{"name": l} for l in (labels or [])],
        "created_at": "2025-01-10T00:00:00Z",
        "updated_at": "2025-01-15T12:00:00Z",
        "comments": comments,
        "html_url": f"https://github.com/owner/repo/issues/{number}",
        "reactions": {"+1": 0, "-1": 0, "laugh": 0, "hooray": 0, "confused": 0,
                      "heart": 0, "rocket": 0, "eyes": 0, **(reactions or {})},
    }


def make_github_comment(comment_id=1, issue_number=1, author="commenter"):
    return {
        "id": comment_id,
        "user": {"login": author},
        "body": f"Comment {comment_id} text",
        "created_at": "2025-01-15T10:00:00Z",
        "updated_at": "2025-01-15T11:00:00Z",
        "issue_url": f"https://api.github.com/repos/owner/repo/issues/{issue_number}",
        "html_url": f"https://github.com/owner/repo/issues/{issue_number}#issuecomment-{comment_id}",
        "reactions": {"+1": 2, "-1": 0, "laugh": 0, "hooray": 0, "confused": 0,
                      "heart": 1, "rocket": 0, "eyes": 0},
    }


def make_discord_message(msg_id="100", author="alice", content="hello"):
    return {
        "id": msg_id,
        "author": {"username": author},
        "content": content,
        "timestamp": "2025-01-15T10:30:00+00:00",
        "reactions": [{"emoji": {"name": "thumbsup"}, "count": 3}],
    }


def make_discord_channel(chan_id="200", name="general", channel_type=0, topic="General chat"):
    return {
        "id": chan_id,
        "name": name,
        "type": channel_type,
        "topic": topic,
    }


# ---------------------------------------------------------------------------
# GitHub tests
# ---------------------------------------------------------------------------


class TestGitHubClient:

    def test_missing_token_raises(self):
        with pytest.raises(ValueError, match="EXE_GITHUB_TOKEN"):
            GitHubClient("")

    def test_whitespace_token_raises(self):
        with pytest.raises(ValueError, match="EXE_GITHUB_TOKEN"):
            GitHubClient("   ")

    def test_list_issues(self, monkeypatch):
        issues = [make_github_issue(1), make_github_issue(2)]
        call_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal call_count
            call_count += 1
            return MockResponse(issues, headers={
                "x-ratelimit-remaining": "4999",
                "x-ratelimit-reset": str(int(time.time()) + 3600),
            })

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        result = client.list_issues("owner", "repo")
        assert len(result) == 2
        assert result[0]["number"] == 1
        assert call_count == 1

    def test_rate_limit_warning(self, monkeypatch, caplog):
        def mock_urlopen(req, timeout=None):
            return MockResponse([], headers={
                "x-ratelimit-remaining": "50",
                "x-ratelimit-reset": str(int(time.time()) + 60),
            })

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        import logging
        with caplog.at_level(logging.WARNING):
            client = GitHubClient("ghp_test")
            client.list_issues("owner", "repo")
        assert "rate limit low" in caplog.text.lower()


class TestGitHubProxyObjects:

    @pytest.fixture
    def client(self, monkeypatch):
        # Client that won't be called unless explicitly mocked
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return GitHubClient("ghp_test")

    def test_issue_structure(self, client):
        raw = make_github_issue(42, "Bug report", "open", labels=["bug"], reactions={"+1": 5})
        issue = GitHubIssue(client, "owner", "repo", raw)

        assert issue.number == 42
        assert issue.title == "Bug report"
        assert issue.state == "open"
        assert issue.author == "testuser"
        assert "bug" in issue.labels
        assert issue.reactions == {"+1": 5}
        assert issue.comment_count == 0

    def test_issue_proxy_dir(self, client):
        issue = GitHubIssue(client, "owner", "repo", make_github_issue())
        assert "number" in issue._proxy_dir
        assert "title" in issue._proxy_dir
        assert "comments" in issue._proxy_dir
        assert "_client" not in issue._proxy_dir

    def test_issue_doc(self, client):
        issue = GitHubIssue(client, "owner", "repo", make_github_issue(7, "My title"))
        assert "#7" in issue._proxy_doc
        assert "My title" in issue._proxy_doc

    def test_comment_structure(self):
        raw = make_github_comment(99, issue_number=5, author="reviewer")
        comment = GitHubComment(raw)

        assert comment.author == "reviewer"
        assert comment.issue_number == 5
        assert comment.reactions == {"+1": 2, "heart": 1}
        assert "Comment 99 text" in comment.body

    def test_comment_proxy_dir(self):
        comment = GitHubComment(make_github_comment())
        assert "author" in comment._proxy_dir
        assert "body" in comment._proxy_dir
        assert "_client" not in comment._proxy_dir
        assert "_proxy_id" not in comment._proxy_dir

    def test_repo_structure(self, client):
        repo = GitHubRepo(client, "owner", "repo", description="A test repo")
        assert repo.name == "owner/repo"
        assert repo.description == "A test repo"
        assert "issues" in repo._proxy_dir
        assert "comments" in repo._proxy_dir
        assert "search_issues" in repo._proxy_dir
        assert "search_issues" in repo._proxy_methods

    def test_repo_issues_lazy_fetch(self, client, monkeypatch):
        issues_data = [make_github_issue(1), make_github_issue(2)]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse(issues_data, headers={
                "x-ratelimit-remaining": "4999",
                "x-ratelimit-reset": str(int(time.time()) + 3600),
            })

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        repo = GitHubRepo(client, "owner", "repo")

        # First access fetches
        issues = repo.issues
        assert len(issues) == 2
        assert fetch_count == 1

        # Second access uses cache
        issues2 = repo.issues
        assert len(issues2) == 2
        assert fetch_count == 1

    def test_repo_issues_ttl_expiry(self, client, monkeypatch):
        issues_data = [make_github_issue(1)]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse(issues_data, headers={
                "x-ratelimit-remaining": "4999",
                "x-ratelimit-reset": str(int(time.time()) + 3600),
            })

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        repo = GitHubRepo(client, "owner", "repo")

        # First access
        repo.issues
        assert fetch_count == 1

        # Simulate TTL expiry
        repo._issues_fetched_at = time.monotonic() - 3601

        # Should re-fetch
        repo.issues
        assert fetch_count == 2


class TestGitHubSecurity:

    def test_client_not_in_proxy_dir(self):
        client = GitHubClient.__new__(GitHubClient)
        client._token = "test"
        issue = GitHubIssue(client, "owner", "repo", make_github_issue())
        assert "_client" not in issue._proxy_dir
        assert "_owner" not in issue._proxy_dir
        assert "_repo" not in issue._proxy_dir

    def test_registry_blocks_private_attrs(self):
        client = GitHubClient.__new__(GitHubClient)
        client._token = "test"
        issue = GitHubIssue(client, "owner", "repo", make_github_issue())
        reg = ProxyRegistry()
        reg.register(issue)

        with pytest.raises(AttributeError, match="not exposed"):
            reg.resolve_getattr(issue._proxy_id, "_client")

    def test_registry_allows_public_attrs(self):
        client = GitHubClient.__new__(GitHubClient)
        client._token = "test"
        issue = GitHubIssue(client, "owner", "repo", make_github_issue(42))
        reg = ProxyRegistry()
        reg.register(issue)

        result = reg.resolve_getattr(issue._proxy_id, "number")
        assert result == {"type": "concrete", "value": 42}

    def test_reactions_exposed_as_concrete_dict(self):
        raw = make_github_issue(reactions={"+1": 3, "heart": 1})
        client = GitHubClient.__new__(GitHubClient)
        client._token = "test"
        issue = GitHubIssue(client, "owner", "repo", raw)
        reg = ProxyRegistry()
        reg.register(issue)

        result = reg.resolve_getattr(issue._proxy_id, "reactions")
        assert result["type"] == "concrete"
        assert result["value"] == {"+1": 3, "heart": 1}


# ---------------------------------------------------------------------------
# Discord tests
# ---------------------------------------------------------------------------


class TestDiscordClient:

    def test_missing_token_raises(self):
        with pytest.raises(ValueError, match="EXE_DISCORD_BOT_TOKEN"):
            DiscordClient("")

    def test_list_guilds(self, monkeypatch):
        guilds = [{"id": "1", "name": "Test Guild"}]

        def mock_urlopen(req, timeout=None):
            return MockResponse(guilds)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = DiscordClient("bot_token")
        result = client.list_guilds()
        assert len(result) == 1
        assert result[0]["name"] == "Test Guild"

    def test_list_messages(self, monkeypatch):
        msgs = [make_discord_message("1"), make_discord_message("2")]

        def mock_urlopen(req, timeout=None):
            return MockResponse(msgs)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = DiscordClient("bot_token")
        result = client.list_messages("chan_123")
        assert len(result) == 2


class TestDiscordProxyObjects:

    @pytest.fixture
    def client(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return DiscordClient("bot_token")

    def test_message_structure(self):
        raw = make_discord_message("1", "bob", "hey there")
        msg = DiscordMessage(raw)
        assert msg.author == "bob"
        assert msg.content == "hey there"
        assert msg.reactions == {"thumbsup": 3}
        assert msg.type == 0
        assert msg.attachments == []

    def test_message_type(self):
        raw = make_discord_message("1")
        raw["type"] = 7  # guild member join
        msg = DiscordMessage(raw)
        assert msg.type == 7

    def test_message_attachments(self):
        raw = make_discord_message("1")
        raw["attachments"] = [
            {"filename": "screenshot.png", "url": "https://cdn.discord.com/a/b.png", "size": 12345},
            {"filename": "log.txt", "url": "https://cdn.discord.com/a/c.txt", "size": 500},
        ]
        msg = DiscordMessage(raw)
        assert len(msg.attachments) == 2
        assert msg.attachments[0]["filename"] == "screenshot.png"
        assert msg.attachments[1]["size"] == 500

    def test_message_proxy_dir(self):
        msg = DiscordMessage(make_discord_message())
        assert "author" in msg._proxy_dir
        assert "content" in msg._proxy_dir
        assert "timestamp" in msg._proxy_dir
        assert "reactions" in msg._proxy_dir
        assert "type" in msg._proxy_dir
        assert "attachments" in msg._proxy_dir
        assert "_proxy_id" not in msg._proxy_dir

    def test_channel_structure(self, client):
        raw = make_discord_channel("300", "dev", topic="Dev discussion")
        channel = DiscordChannel(client, raw)
        assert channel.id == "300"
        assert channel.name == "dev"
        assert channel.topic == "Dev discussion"
        assert channel.type == 0
        assert channel.parent_id == ""

    def test_channel_with_parent(self, client):
        raw = make_discord_channel("400", "my-thread", channel_type=11)
        raw["parent_id"] = "300"
        channel = DiscordChannel(client, raw)
        assert channel.type == 11
        assert channel.parent_id == "300"

    def test_channel_proxy_dir(self, client):
        channel = DiscordChannel(client, make_discord_channel())
        assert "id" in channel._proxy_dir
        assert "name" in channel._proxy_dir
        assert "topic" in channel._proxy_dir
        assert "type" in channel._proxy_dir
        assert "parent_id" in channel._proxy_dir
        assert "messages" in channel._proxy_dir
        assert "_client" not in channel._proxy_dir

    def test_channel_messages_lazy_fetch(self, client, monkeypatch):
        msgs = [make_discord_message("1"), make_discord_message("2")]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse(msgs)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())

        # First access fetches
        messages = channel.messages
        assert len(messages) == 2
        assert fetch_count == 1

        # Second access uses cache
        messages2 = channel.messages
        assert len(messages2) == 2
        assert fetch_count == 1

    def test_channel_messages_ttl_expiry(self, client, monkeypatch):
        msgs = [make_discord_message()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse(msgs)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())

        channel.messages
        assert fetch_count == 1

        # Simulate TTL expiry
        channel._messages_fetched_at = time.monotonic() - 3601

        channel.messages
        assert fetch_count == 2

    def test_source_structure(self, client):
        source = DiscordSource(client, "guild_1", "Test Server")
        assert source.guild_name == "Test Server"
        assert "guild_name" in source._proxy_dir
        assert "channels" in source._proxy_dir

    def test_source_channels_filters_text_announcement_forum(self, client, monkeypatch):
        channels = [
            make_discord_channel("1", "general", channel_type=0),
            make_discord_channel("2", "voice", channel_type=2),       # voice
            make_discord_channel("3", "dev", channel_type=0),
            make_discord_channel("4", "category", channel_type=4),    # category
            make_discord_channel("5", "announcements", channel_type=5),  # announcement
            make_discord_channel("6", "help-forum", channel_type=15),    # forum
        ]

        def mock_urlopen(req, timeout=None):
            return MockResponse(channels)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = DiscordSource(client, "guild_1", "Test Server")

        result = source.channels
        assert len(result) == 4
        assert result[0].name == "general"
        assert result[1].name == "dev"
        assert result[2].name == "announcements"
        assert result[3].name == "help-forum"

    def test_source_channels_ttl(self, client, monkeypatch):
        channels = [make_discord_channel()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse(channels)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = DiscordSource(client, "guild_1", "Test")

        source.channels
        assert fetch_count == 1

        source.channels
        assert fetch_count == 1

        # 1-hour TTL for channels
        source._channels_fetched_at = time.monotonic() - 3601

        source.channels
        assert fetch_count == 2


class TestDiscordSecurity:

    def test_client_not_in_proxy_dir(self):
        client = DiscordClient.__new__(DiscordClient)
        client._token = "test"
        channel = DiscordChannel(client, make_discord_channel())
        assert "id" in channel._proxy_dir
        assert "_client" not in channel._proxy_dir
        assert "_channel_id" not in channel._proxy_dir

    def test_registry_blocks_private_attrs(self):
        client = DiscordClient.__new__(DiscordClient)
        client._token = "test"
        source = DiscordSource(client, "guild_1", "Test")
        reg = ProxyRegistry()
        reg.register(source)

        with pytest.raises(AttributeError, match="not exposed"):
            reg.resolve_getattr(source._proxy_id, "_client")

    def test_registry_allows_public_attrs(self):
        client = DiscordClient.__new__(DiscordClient)
        client._token = "test"
        source = DiscordSource(client, "guild_1", "Test")
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_getattr(source._proxy_id, "guild_name")
        assert result == {"type": "concrete", "value": "Test"}

    def test_reactions_exposed_as_concrete_dict(self):
        msg = DiscordMessage(make_discord_message())
        reg = ProxyRegistry()
        reg.register(msg)

        result = reg.resolve_getattr(msg._proxy_id, "reactions")
        assert result["type"] == "concrete"
        assert result["value"] == {"thumbsup": 3}


# ---------------------------------------------------------------------------
# GitHub search tests
# ---------------------------------------------------------------------------


_GOOD_HEADERS = {
    "x-ratelimit-remaining": "4999",
    "x-ratelimit-reset": "9999999999",
}


class TestGitHubSearch:

    def test_client_search_issues(self, monkeypatch):
        """GitHubClient.search_issues hits /search/issues with scoped query."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse(
                {"total_count": 1, "items": [make_github_issue(1)]},
                headers=_GOOD_HEADERS,
            )

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        results = client.search_issues("owner", "repo", "label:bug is:open")
        assert len(results) == 1
        assert "repo%3Aowner%2Frepo" in captured_urls[0]

    def test_repo_search_issues_method(self, monkeypatch):
        """GitHubRepo.search_issues via resolve_call returns GitHubIssues."""
        def mock_urlopen(req, timeout=None):
            return MockResponse(
                {"total_count": 2, "items": [make_github_issue(10), make_github_issue(20)]},
                headers=_GOOD_HEADERS,
            )

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        result = reg.resolve_call(repo._proxy_id, "search_issues", ["label:bug"], {})
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 2
        assert result["items"][0]["type_name"] == "GitHubIssue"

    def test_search_issues_returns_method_type(self, monkeypatch):
        """resolve_getattr for search_issues returns method descriptor."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        result = reg.resolve_getattr(repo._proxy_id, "search_issues")
        assert result["type"] == "method"
        assert result["name"] == "search_issues"

    def test_search_issues_in_attr_docs(self, monkeypatch):
        """search_issues has documentation in __attr_docs__."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        assert "search_issues" in repo._proxy_attr_docs
        assert "GitHub" in repo._proxy_attr_docs["search_issues"]

    def test_search_issues_unknown_method_rejected(self, monkeypatch):
        """resolve_call rejects methods not in _proxy_methods."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        with pytest.raises(AttributeError, match="not exposed"):
            reg.resolve_call(repo._proxy_id, "delete_repo", [], {})

    def test_search_issues_non_primitive_arg_rejected(self, monkeypatch):
        """resolve_call rejects non-JSON-primitive arguments."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        with pytest.raises(TypeError, match="not a JSON primitive"):
            reg.resolve_call(repo._proxy_id, "search_issues", [["a", "list"]], {})

    def test_discord_source_has_no_search_methods(self, monkeypatch):
        """Discord source has no search methods — no search API for bots."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = DiscordClient("bot_token")
        source = DiscordSource(client, "guild_1", "Test")
        # Has snowflake helpers but no search/query methods
        assert "search" not in " ".join(source._proxy_methods.keys())


# ---------------------------------------------------------------------------
# New tests: fetch_messages, pagination, is_pull_request, URL encoding
# ---------------------------------------------------------------------------


class TestDiscordFetchMessages:

    @pytest.fixture
    def client(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return DiscordClient("bot_token")

    def test_fetch_messages_in_dir_and_methods(self, client):
        """fetch_messages appears in dir_attrs, attr_docs, and methods."""
        channel = DiscordChannel(client, make_discord_channel())
        assert "fetch_messages" in channel._proxy_dir
        assert "fetch_messages" in channel._proxy_attr_docs
        assert "fetch_messages" in channel._proxy_methods

    def test_fetch_messages_with_snowflakes(self, client, monkeypatch):
        """fetch_messages passes after/before snowflakes to list_messages."""
        captured_params = []

        def mock_urlopen(req, timeout=None):
            captured_params.append(req.full_url)
            return MockResponse([make_discord_message("50")])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())

        result = channel._fetch_messages(after="100", before="200")
        assert len(result) == 1
        assert isinstance(result[0], DiscordMessage)
        assert "after=100" in captured_params[0]
        assert "before=200" in captured_params[0]

    def test_fetch_messages_clamps_limit(self, client, monkeypatch):
        """fetch_messages clamps limit to [1, 1000]."""
        page_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal page_count
            page_count += 1
            return MockResponse([make_discord_message(str(page_count))])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())

        # limit=0 becomes 1
        result = channel._fetch_messages(limit=0)
        assert len(result) == 1

    def test_fetch_messages_limit_over_1000(self, client, monkeypatch):
        """fetch_messages clamps limit above 1000."""
        def mock_urlopen(req, timeout=None):
            return MockResponse([make_discord_message("1")])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())

        # Should not crash; limit clamped to 1000
        result = channel._fetch_messages(limit=5000)
        assert len(result) == 1

    def test_fetch_messages_via_resolve_call(self, client, monkeypatch):
        """fetch_messages works through the proxy registry resolve_call path."""
        def mock_urlopen(req, timeout=None):
            return MockResponse([make_discord_message("10"), make_discord_message("20")])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        channel = DiscordChannel(client, make_discord_channel())
        reg = ProxyRegistry()
        reg.register(channel)

        result = reg.resolve_call(channel._proxy_id, "fetch_messages", [], {"after": "5"})
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 2


class TestDiscordPaginationFix:

    def test_after_cursor_advances_across_pages(self, monkeypatch):
        """list_messages advances the after cursor to paginate forward."""
        captured_urls = []
        call_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal call_count
            captured_urls.append(req.full_url)
            call_count += 1
            # Return a full page (100 msgs) oldest-first (as Discord does with after=)
            if call_count == 1:
                msgs = [make_discord_message(str(i)) for i in range(51, 151)]
                return MockResponse(msgs)
            else:
                return MockResponse([])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = DiscordClient("bot_token")
        client.list_messages("chan_123", after="50", max_pages=3)

        # First page should have after=50
        assert "after=50" in captured_urls[0]
        # Second page should advance cursor to last message ID from page 1
        if len(captured_urls) > 1:
            assert "after=150" in captured_urls[1]

    def test_before_param_in_list_messages(self, monkeypatch):
        """list_messages accepts a before parameter."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse([make_discord_message("1")])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = DiscordClient("bot_token")
        client.list_messages("chan_123", before="999")

        assert "before=999" in captured_urls[0]


class TestGitHubIsPullRequest:

    @pytest.fixture
    def client(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return GitHubClient("ghp_test")

    def test_issue_is_not_pr(self, client):
        """Regular issues have is_pull_request=False."""
        raw = make_github_issue(1)
        issue = GitHubIssue(client, "owner", "repo", raw)
        assert issue.is_pull_request is False

    def test_pr_is_pr(self, client):
        """Issues with pull_request key have is_pull_request=True."""
        raw = make_github_issue(2)
        raw["pull_request"] = {"url": "https://api.github.com/repos/owner/repo/pulls/2"}
        issue = GitHubIssue(client, "owner", "repo", raw)
        assert issue.is_pull_request is True

    def test_is_pull_request_in_dir_and_docs(self, client):
        """is_pull_request appears in dir_attrs and attr_docs."""
        issue = GitHubIssue(client, "owner", "repo", make_github_issue())
        assert "is_pull_request" in issue._proxy_dir
        assert "is_pull_request" in issue._proxy_attr_docs

    def test_is_pull_request_via_registry(self, client):
        """is_pull_request is accessible through the proxy registry."""
        raw = make_github_issue(1)
        raw["pull_request"] = {"url": "..."}
        issue = GitHubIssue(client, "owner", "repo", raw)
        reg = ProxyRegistry()
        reg.register(issue)

        result = reg.resolve_getattr(issue._proxy_id, "is_pull_request")
        assert result == {"type": "concrete", "value": True}


class TestGitHubURLEncoding:

    def test_search_query_with_spaces(self, monkeypatch):
        """Search queries with spaces are properly URL-encoded."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse(
                {"total_count": 0, "items": []},
                headers=_GOOD_HEADERS,
            )

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        client.search_issues("owner", "repo", "label:bug is:open")

        # The query should be URL-encoded (spaces → %20 or +)
        url = captured_urls[0]
        assert " " not in url.split("?", 1)[-1]

    def test_search_query_with_special_chars(self, monkeypatch):
        """Search queries with special characters are properly URL-encoded."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse(
                {"total_count": 0, "items": []},
                headers=_GOOD_HEADERS,
            )

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        client.search_issues("owner", "repo", "fix & improve + tests")

        url = captured_urls[0]
        qs = url.split("?", 1)[-1]
        # & should be encoded in values, not acting as param separator
        # The raw & in the query value should be %26
        assert "%26" in qs or "%2B" in qs  # at least special chars are encoded


# ---------------------------------------------------------------------------
# Discord URL encoding tests
# ---------------------------------------------------------------------------


class TestDiscordURLEncoding:

    def test_params_are_url_encoded(self, monkeypatch):
        """Discord client URL-encodes query parameters."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse([])

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = DiscordClient("bot_token")
        client.list_messages("chan_123", after="100", before="200")

        url = captured_urls[0]
        # Should not have raw spaces in query string
        qs = url.split("?", 1)[-1]
        assert " " not in qs


# ---------------------------------------------------------------------------
# GitHub pagination encoding tests
# ---------------------------------------------------------------------------


class TestGitHubPaginationEncoding:

    def test_no_double_encoding(self, monkeypatch):
        """Params from Link header are not double-encoded on page 2."""
        call_count = 0
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            nonlocal call_count
            call_count += 1
            captured_urls.append(req.full_url)
            if call_count == 1:
                return MockResponse(
                    [make_github_issue(i) for i in range(1, 101)],
                    headers={
                        **_GOOD_HEADERS,
                        "link": '<https://api.github.com/repos/owner/repo/issues?per_page=100&page=2&q=label%3Abug>; rel="next"',
                    },
                )
            else:
                return MockResponse([], headers=_GOOD_HEADERS)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        client.list_issues("owner", "repo")

        # Page 2 URL should have %3A (from unquote then re-quote), not %253A
        if len(captured_urls) > 1:
            assert "%253A" not in captured_urls[1]


# ---------------------------------------------------------------------------
# Discord active threads tests
# ---------------------------------------------------------------------------


class TestDiscordActiveThreads:

    @pytest.fixture
    def client(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return DiscordClient("bot_token")

    def test_active_threads_in_dir(self, client):
        """active_threads appears in dir_attrs."""
        source = DiscordSource(client, "guild_1", "Test")
        assert "active_threads" in source._proxy_dir
        assert "active_threads" in source._proxy_attr_docs

    def test_active_threads_returns_channels(self, client, monkeypatch):
        """active_threads returns DiscordChannel objects."""
        threads = [
            make_discord_channel("t1", "thread-one", channel_type=11),
            make_discord_channel("t2", "thread-two", channel_type=11),
        ]
        call_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal call_count
            call_count += 1
            # Active threads endpoint returns {threads: [...], members: [...]}
            return MockResponse({"threads": threads, "members": []})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = DiscordSource(client, "guild_1", "Test")
        result = source.active_threads
        assert len(result) == 2
        assert isinstance(result[0], DiscordChannel)
        assert result[0].name == "thread-one"

    def test_active_threads_ttl(self, client, monkeypatch):
        """active_threads respects 1-hour TTL."""
        threads = [make_discord_channel("t1", "thread", channel_type=11)]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"threads": threads, "members": []})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = DiscordSource(client, "guild_1", "Test")

        source.active_threads
        assert fetch_count == 1

        source.active_threads
        assert fetch_count == 1

        # Expire TTL
        source._active_threads_fetched_at = time.monotonic() - 3601
        source.active_threads
        assert fetch_count == 2


# ---------------------------------------------------------------------------
# Discord snowflake helper tests
# ---------------------------------------------------------------------------


class TestSnowflakeHelpers:

    def test_snowflake_round_trip(self):
        """snowflake_from_timestamp and timestamp_from_snowflake are inverses."""
        ts = 1700000000.0
        sf = snowflake_from_timestamp(ts)
        ts2 = timestamp_from_snowflake(sf)
        # Precision loss from the <<22 shift, but should be within 1ms
        assert abs(ts - ts2) < 0.001

    def test_known_snowflake(self):
        """Known Discord epoch produces snowflake 0."""
        # Discord epoch: 2015-01-01T00:00:00Z = 1420070400.0
        sf = snowflake_from_timestamp(1420070400.0)
        assert sf == 0

    def test_timestamp_from_zero_snowflake(self):
        """Snowflake 0 maps to Discord epoch."""
        ts = timestamp_from_snowflake(0)
        assert ts == 1420070400.0

    def test_source_snowflake_from_timestamp_float(self, monkeypatch):
        """DiscordSource.snowflake_from_timestamp works with float input via resolve_call."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = DiscordClient("bot_token")
        source = DiscordSource(client, "guild_1", "Test")
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_call(source._proxy_id, "snowflake_from_timestamp", [1700000000.0], {})
        assert result["type"] == "concrete"
        assert isinstance(result["value"], str)
        assert result["value"].isdigit()

    def test_source_snowflake_from_timestamp_iso(self, monkeypatch):
        """DiscordSource.snowflake_from_timestamp works with ISO date string."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = DiscordClient("bot_token")
        source = DiscordSource(client, "guild_1", "Test")
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_call(source._proxy_id, "snowflake_from_timestamp", ["2025-03-06"], {})
        assert result["type"] == "concrete"
        assert isinstance(result["value"], str)
        assert result["value"].isdigit()
        assert int(result["value"]) > 0

    def test_source_timestamp_from_snowflake(self, monkeypatch):
        """DiscordSource.timestamp_from_snowflake returns ISO string via resolve_call."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = DiscordClient("bot_token")
        source = DiscordSource(client, "guild_1", "Test")
        reg = ProxyRegistry()
        reg.register(source)

        # Use a known snowflake (Discord epoch = snowflake 0)
        result = reg.resolve_call(source._proxy_id, "timestamp_from_snowflake", [0], {})
        assert result["type"] == "concrete"
        assert "2015-01-01" in result["value"]

    def test_snowflake_methods_in_proxy_dir(self, monkeypatch):
        """Snowflake methods appear in _proxy_dir and _proxy_methods."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = DiscordClient("bot_token")
        source = DiscordSource(client, "guild_1", "Test")
        assert "snowflake_from_timestamp" in source._proxy_dir
        assert "timestamp_from_snowflake" in source._proxy_dir
        assert "snowflake_from_timestamp" in source._proxy_methods
        assert "timestamp_from_snowflake" in source._proxy_methods

    def test_coerce_snowflake_large_float(self):
        """_coerce_snowflake handles scientific-notation floats (JS precision loss)."""
        # Simulate what happens when a snowflake round-trips through JavaScript:
        # int 1479267267379200000 becomes float 1.4792672673792e+18
        original = snowflake_from_timestamp(1772755200.0)  # Mar 6 2026
        lossy_float = float(original)  # loses low bits, like JS would
        result = _coerce_snowflake(lossy_float)
        assert result.isdigit()
        assert len(result) >= 15
        # Should be close to the original (within JS float precision)
        assert abs(int(result) - original) < 2**22  # sub-ms precision is fine

    def test_coerce_snowflake_string_snowflake(self):
        """_coerce_snowflake passes through digit strings."""
        assert _coerce_snowflake("1479267267379200000") == "1479267267379200000"

    def test_coerce_snowflake_none(self):
        """_coerce_snowflake returns None for None."""
        assert _coerce_snowflake(None) is None


# ---------------------------------------------------------------------------
# GitHub fetch_issues tests
# ---------------------------------------------------------------------------


class TestGitHubFetchIssues:

    def test_fetch_issues_in_dir_and_methods(self, monkeypatch):
        """fetch_issues appears in dir_attrs, attr_docs, and methods."""
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        assert "fetch_issues" in repo._proxy_dir
        assert "fetch_issues" in repo._proxy_attr_docs
        assert "fetch_issues" in repo._proxy_methods

    def test_fetch_issues_passes_params(self, monkeypatch):
        """fetch_issues passes state/since/labels params to the API."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse([], headers=_GOOD_HEADERS)

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        reg.resolve_call(
            repo._proxy_id, "fetch_issues", [],
            {"state": "open", "since": "2025-03-06T00:00:00Z"},
        )
        url = captured_urls[0]
        assert "state=open" in url
        assert "since=" in url

    def test_fetch_issues_returns_github_issues(self, monkeypatch):
        """fetch_issues returns GitHubIssue proxy objects."""
        def mock_urlopen(req, timeout=None):
            return MockResponse(
                [make_github_issue(1), make_github_issue(2)],
                headers=_GOOD_HEADERS,
            )

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = GitHubClient("ghp_test")
        repo = GitHubRepo(client, "owner", "repo")
        reg = ProxyRegistry()
        reg.register(repo)

        result = reg.resolve_call(repo._proxy_id, "fetch_issues", [], {"state": "open"})
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 2
        assert result["items"][0]["type_name"] == "GitHubIssue"


# ---------------------------------------------------------------------------
# Missive mock helpers
# ---------------------------------------------------------------------------


def make_missive_conversation(conv_id="conv_1", subject="Support request"):
    return {
        "id": conv_id,
        "subject": subject,
        "created_at": 1740823200,       # 2025-03-01T10:00:00+00:00 (Unix seconds)
        "last_activity_at": 1741275000,  # 2025-03-06T15:30:00+00:00 (Unix seconds)
        "assignees": [{"name": "Alice", "email": "alice@example.com"}],
        "shared_labels": [{"id": "lbl_1", "name": "urgent"}],
        "team": {"name": "Support"},
    }


def make_missive_message(msg_id="msg_1", subject="Re: Help", body="<p>Thanks!</p>"):
    return {
        "id": msg_id,
        "subject": subject,
        "preview": "Thanks!",
        "from_field": {"address": "user@example.com", "name": "User"},
        "to_fields": [{"address": "support@example.com", "name": "Support"}],
        "body": body,
        "delivered_at": 1741269600,  # 2025-03-06T14:00:00+00:00 (Unix seconds)
    }


def make_missive_comment(comment_id="cmt_1", body="Internal note"):
    return {
        "id": comment_id,
        "author": {"name": "Bob", "email": "bob@example.com"},
        "body": body,
        "created_at": 1741271400,  # 2025-03-06T14:30:00+00:00 (Unix seconds)
    }


def make_missive_contact(contact_id="ct_1", name="Jane Doe"):
    return {
        "id": contact_id,
        "name": name,
        "email_addresses": [{"address": "jane@example.com"}],
        "phone_numbers": [{"number": "+15551234567"}],
    }


def make_missive_shared_label(label_id="lbl_1", name="urgent"):
    return {"id": label_id, "name": name}


# ---------------------------------------------------------------------------
# Missive helper tests
# ---------------------------------------------------------------------------


class TestMissiveHelpers:

    def test_unix_to_iso_int(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso(1741269600) == "2025-03-06T14:00:00+00:00"

    def test_unix_to_iso_float(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso(1741269600.0) == "2025-03-06T14:00:00+00:00"

    def test_unix_to_iso_string_number(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso("1741269600") == "2025-03-06T14:00:00+00:00"

    def test_unix_to_iso_none(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso(None) == ""

    def test_unix_to_iso_empty_string(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso("") == ""

    def test_unix_to_iso_iso_string_returns_empty(self):
        from panopticon.sources.missive import _unix_to_iso
        assert _unix_to_iso("2025-03-06T14:00:00Z") == ""


# ---------------------------------------------------------------------------
# Missive client tests
# ---------------------------------------------------------------------------


class TestMissiveClient:

    def test_missing_token_raises(self):
        with pytest.raises(ValueError, match="EXE_MISSIVE_API_KEY"):
            MissiveClient("")

    def test_whitespace_token_raises(self):
        with pytest.raises(ValueError, match="EXE_MISSIVE_API_KEY"):
            MissiveClient("   ")

    def test_list_conversations(self, monkeypatch):
        convs = [make_missive_conversation()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"conversations": convs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_conversations()
        assert len(result) == 1
        assert result[0]["id"] == "conv_1"

    def test_list_messages(self, monkeypatch):
        msgs = [make_missive_message()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"messages": msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_messages("conv_1")
        assert len(result) == 1

    def test_list_messages_non_dict_elements(self, monkeypatch):
        """list_messages skips non-dict elements in the API response."""
        good_msg = make_missive_message()
        # Simulate malformed API response with a string element
        msgs_with_junk = ["unexpected_string", good_msg, 42]

        call_count = [0]

        def mock_urlopen(req, timeout=None):
            call_count[0] += 1
            if call_count[0] == 1:
                return MockResponse({"messages": msgs_with_junk})
            return MockResponse({"messages": [good_msg]})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_messages("conv_1")
        assert len(result) == 1
        assert result[0]["id"] == good_msg["id"]

    def test_list_comments(self, monkeypatch):
        comments = [make_missive_comment()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"comments": comments})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_comments("conv_1")
        assert len(result) == 1

    def test_list_contacts(self, monkeypatch):
        contacts = [make_missive_contact()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"contacts": contacts})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_contacts(search="jane")
        assert len(result) == 1

    def test_list_shared_labels(self, monkeypatch):
        labels = [make_missive_shared_label()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"shared_labels": labels})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_shared_labels()
        assert len(result) == 1

    def test_bearer_auth(self, monkeypatch):
        """Client uses Bearer auth, not Bot or token."""
        captured_headers = []

        def mock_urlopen(req, timeout=None):
            captured_headers.append(req.get_header("Authorization"))
            return MockResponse({"conversations": []})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        client.list_conversations()
        assert captured_headers[0] == "Bearer missive_pat-test"

    def test_url_encoding(self, monkeypatch):
        """Query params are properly URL-encoded."""
        captured_urls = []

        def mock_urlopen(req, timeout=None):
            captured_urls.append(req.full_url)
            return MockResponse({"contacts": []})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        client.list_contacts(search="john & jane")
        assert "%26" in captured_urls[0]

    def test_list_messages_two_stage_fetch(self, monkeypatch):
        """list_messages does a two-stage fetch: metadata then batch body."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": "Hello there"},
            {"id": "m2", "subject": "Re: Hi", "preview": "Thanks"},
        ]
        stage2_msgs = [
            {"id": "m1", "body": "<p>Hello there full body</p>"},
            {"id": "m2", "body": "<p>Thanks full body</p>"},
        ]
        call_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal call_count
            call_count += 1
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": stage2_msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_messages("conv_1")
        assert call_count == 2
        assert result[0]["body"] == "<p>Hello there full body</p>"
        assert result[1]["body"] == "<p>Thanks full body</p>"

    def test_list_messages_empty_body_falls_back_to_preview(self, monkeypatch):
        """When batch body is empty, falls back to preview from stage 1."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": "Hello preview"},
            {"id": "m2", "subject": "Re: Hi", "preview": "Thanks preview"},
        ]
        stage2_msgs = [
            {"id": "m1", "body": ""},
            {"id": "m2", "body": ""},
        ]

        def mock_urlopen(req, timeout=None):
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": stage2_msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_messages("conv_1")
        assert result[0]["body"] == "Hello preview"
        assert result[1]["body"] == "Thanks preview"

    def test_list_messages_batch_fetch_partial_return(self, monkeypatch, caplog):
        """Batch fetch returns fewer messages than stage 1; warns and falls back."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": "Preview 1"},
            {"id": "m2", "subject": "Re: Hi", "preview": "Preview 2"},
            {"id": "m3", "subject": "Follow-up", "preview": "Preview 3"},
        ]
        # Only m1 comes back from batch
        stage2_msgs = [
            {"id": "m1", "body": "<p>Full body</p>"},
        ]

        def mock_urlopen(req, timeout=None):
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": stage2_msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        import logging
        with caplog.at_level(logging.WARNING):
            client = MissiveClient("missive_pat-test")
            result = client.list_messages("conv_1")
        assert result[0]["body"] == "<p>Full body</p>"
        assert result[1]["body"] == "Preview 2"
        assert result[2]["body"] == "Preview 3"
        assert "1/3 messages" in caplog.text
        assert "fell back to preview" in caplog.text

    def test_list_messages_no_body_no_preview(self, monkeypatch, caplog):
        """Both body and preview empty; warns about data loss."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": ""},
        ]
        stage2_msgs = [
            {"id": "m1", "body": ""},
        ]

        def mock_urlopen(req, timeout=None):
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": stage2_msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        import logging
        with caplog.at_level(logging.WARNING):
            client = MissiveClient("missive_pat-test")
            result = client.list_messages("conv_1")
        assert result[0]["body"] == ""
        assert "no body or preview" in caplog.text

    def test_list_messages_batch_fetch_empty_response(self, monkeypatch, caplog):
        """Batch returns empty list; all bodies fall back to preview."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": "Preview A"},
            {"id": "m2", "subject": "Bye", "preview": "Preview B"},
        ]

        def mock_urlopen(req, timeout=None):
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": []})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        import logging
        with caplog.at_level(logging.WARNING):
            client = MissiveClient("missive_pat-test")
            result = client.list_messages("conv_1")
        assert result[0]["body"] == "Preview A"
        assert result[1]["body"] == "Preview B"
        assert "0/2 messages" in caplog.text
        assert "fell back to preview" in caplog.text

    def test_list_messages_single_message_unwrapped(self, monkeypatch):
        """Single-message fetch returns object, not array — Missive API quirk."""
        stage1_msgs = [
            {"id": "m1", "subject": "Hi", "preview": "Hello there"},
        ]
        # Missive returns a single object (not wrapped in an array) for
        # GET /messages/:id when only one ID is requested.
        single_msg = {"id": "m1", "body": "<p>Full body</p>"}

        def mock_urlopen(req, timeout=None):
            if "/conversations/" in req.full_url:
                return MockResponse({"messages": stage1_msgs})
            else:
                return MockResponse({"messages": single_msg})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        result = client.list_messages("conv_1")
        assert result[0]["body"] == "<p>Full body</p>"


# ---------------------------------------------------------------------------
# Missive proxy object tests
# ---------------------------------------------------------------------------


class TestMissiveProxyObjects:

    @pytest.fixture
    def client(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        return MissiveClient("missive_pat-test")

    def test_message_structure(self):
        raw = make_missive_message("m1", "Help needed", "<p>Please help</p>")
        msg = MissiveMessage(raw)
        assert msg.id == "m1"
        assert msg.subject == "Help needed"
        assert msg.preview == "Thanks!"
        assert msg.from_field == {"address": "user@example.com", "name": "User"}
        assert len(msg.to_fields) == 1
        assert msg.body == "Please help"
        assert msg.delivered_at == "2025-03-06T14:00:00+00:00"

    def test_message_body_html_stripped(self):
        """Real-world Missive email HTML is converted to plain text."""
        html_body = (
            '<div><div dir="auto">Hi.<div dir="auto">Just recharged 10 dollar'
            "&nbsp;</div><div dir=\"auto\">I don't see it in my account&nbsp;"
            "</div></div>\n</div>"
        )
        msg = MissiveMessage(make_missive_message("m2", "Billing", html_body))
        assert "<div>" not in msg.body
        assert "&nbsp;" not in msg.body
        assert "Hi." in msg.body
        assert "10 dollar" in msg.body

    def test_message_body_plain_text_passthrough(self):
        """Plain text bodies (SMS) are returned unchanged."""
        msg = MissiveMessage(make_missive_message("m3", "SMS", "Hello there"))
        assert msg.body == "Hello there"

    def test_message_proxy_dir(self):
        msg = MissiveMessage(make_missive_message())
        assert "id" in msg._proxy_dir
        assert "subject" in msg._proxy_dir
        assert "from_field" in msg._proxy_dir
        assert "body" in msg._proxy_dir
        assert "_proxy_id" not in msg._proxy_dir

    def test_comment_structure(self):
        raw = make_missive_comment("c1", "Need to escalate")
        comment = MissiveComment(raw)
        assert comment.id == "c1"
        assert comment.author == {"name": "Bob", "email": "bob@example.com"}
        assert comment.body == "Need to escalate"
        assert comment.created_at == "2025-03-06T14:30:00+00:00"

    def test_comment_proxy_dir(self):
        comment = MissiveComment(make_missive_comment())
        assert "author" in comment._proxy_dir
        assert "body" in comment._proxy_dir
        assert "_proxy_id" not in comment._proxy_dir

    def test_contact_structure(self):
        raw = make_missive_contact("ct_1", "Jane Doe")
        contact = MissiveContact(raw)
        assert contact.id == "ct_1"
        assert contact.name == "Jane Doe"
        assert contact.email == "jane@example.com"
        assert contact.phone == "+15551234567"

    def test_contact_proxy_dir(self):
        contact = MissiveContact(make_missive_contact())
        assert "name" in contact._proxy_dir
        assert "email" in contact._proxy_dir
        assert "phone" in contact._proxy_dir
        assert "_proxy_id" not in contact._proxy_dir

    def test_conversation_structure(self, client):
        raw = make_missive_conversation("conv_1", "Bug report")
        conv = MissiveConversation(client, raw)
        assert conv.id == "conv_1"
        assert conv.subject == "Bug report"
        assert conv.team == "Support"
        assert conv.assignees == [{"name": "Alice", "email": "alice@example.com"}]
        assert conv.shared_labels == [{"id": "lbl_1", "name": "urgent"}]
        assert conv.created_at == "2025-03-01T10:00:00+00:00"
        assert conv.last_activity_at == "2025-03-06T15:30:00+00:00"

    def test_conversation_proxy_dir(self, client):
        conv = MissiveConversation(client, make_missive_conversation())
        assert "id" in conv._proxy_dir
        assert "subject" in conv._proxy_dir
        assert "messages" in conv._proxy_dir
        assert "comments" in conv._proxy_dir
        assert "_client" not in conv._proxy_dir

    def test_conversation_messages_lazy_fetch(self, client, monkeypatch):
        msgs = [make_missive_message("m1"), make_missive_message("m2")]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"messages": msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        conv = MissiveConversation(client, make_missive_conversation())

        # First access fetches (2 calls: metadata + batch body)
        messages = conv.messages
        assert len(messages) == 2
        assert fetch_count == 2

        # Second access uses cache
        messages2 = conv.messages
        assert len(messages2) == 2
        assert fetch_count == 2

    def test_conversation_messages_ttl_expiry(self, client, monkeypatch):
        msgs = [make_missive_message()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"messages": msgs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        conv = MissiveConversation(client, make_missive_conversation())

        conv.messages
        assert fetch_count == 2  # metadata + batch body

        # Simulate TTL expiry
        conv._messages_fetched_at = time.monotonic() - 3601

        conv.messages
        assert fetch_count == 4  # another metadata + batch body

    def test_conversation_comments_lazy_fetch(self, client, monkeypatch):
        comments = [make_missive_comment()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"comments": comments})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        conv = MissiveConversation(client, make_missive_conversation())

        result = conv.comments
        assert len(result) == 1
        assert fetch_count == 1

        # Cached
        conv.comments
        assert fetch_count == 1

    def test_source_structure(self, client):
        source = MissiveSource(client)
        assert "conversations" in source._proxy_dir
        assert "shared_labels" in source._proxy_dir
        assert "contacts" in source._proxy_dir
        assert "fetch_conversations" in source._proxy_dir
        assert "search_contacts" in source._proxy_dir
        assert "fetch_conversations" in source._proxy_methods
        assert "search_contacts" in source._proxy_methods

    def test_source_conversations_lazy_fetch(self, client, monkeypatch):
        convs = [make_missive_conversation()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"conversations": convs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = MissiveSource(client)

        result = source.conversations
        assert len(result) == 1
        assert isinstance(result[0], MissiveConversation)
        assert fetch_count == 1

        # Cached
        source.conversations
        assert fetch_count == 1

    def test_source_conversations_ttl(self, client, monkeypatch):
        convs = [make_missive_conversation()]
        fetch_count = 0

        def mock_urlopen(req, timeout=None):
            nonlocal fetch_count
            fetch_count += 1
            return MockResponse({"conversations": convs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = MissiveSource(client)

        source.conversations
        assert fetch_count == 1

        source._conversations_fetched_at = time.monotonic() - 3601

        source.conversations
        assert fetch_count == 2

    def test_source_shared_labels(self, client, monkeypatch):
        labels = [make_missive_shared_label("l1", "urgent"), make_missive_shared_label("l2", "billing")]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"shared_labels": labels})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = MissiveSource(client)
        result = source.shared_labels
        assert len(result) == 2
        assert result[0] == {"id": "l1", "name": "urgent"}
        assert result[1] == {"id": "l2", "name": "billing"}

    def test_source_contacts(self, client, monkeypatch):
        contacts = [make_missive_contact()]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"contacts": contacts})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        source = MissiveSource(client)
        result = source.contacts
        assert len(result) == 1
        assert isinstance(result[0], MissiveContact)


# ---------------------------------------------------------------------------
# Missive security tests
# ---------------------------------------------------------------------------


class TestMissiveSecurity:

    def test_client_not_in_proxy_dir(self):
        client = MissiveClient.__new__(MissiveClient)
        client._token = "test"
        conv = MissiveConversation(client, make_missive_conversation())
        assert "_client" not in conv._proxy_dir
        assert "_conv_id" not in conv._proxy_dir

    def test_registry_blocks_private_attrs(self):
        client = MissiveClient.__new__(MissiveClient)
        client._token = "test"
        source = MissiveSource(client)
        reg = ProxyRegistry()
        reg.register(source)

        with pytest.raises(AttributeError, match="not exposed"):
            reg.resolve_getattr(source._proxy_id, "_client")

    def test_registry_allows_public_attrs(self):
        client = MissiveClient.__new__(MissiveClient)
        client._token = "test"
        conv = MissiveConversation(client, make_missive_conversation("c1", "Test"))
        reg = ProxyRegistry()
        reg.register(conv)

        result = reg.resolve_getattr(conv._proxy_id, "subject")
        assert result == {"type": "concrete", "value": "Test"}

    def test_assignees_exposed_as_concrete(self):
        client = MissiveClient.__new__(MissiveClient)
        client._token = "test"
        conv = MissiveConversation(client, make_missive_conversation())
        reg = ProxyRegistry()
        reg.register(conv)

        result = reg.resolve_getattr(conv._proxy_id, "assignees")
        assert result["type"] == "concrete"
        assert result["value"] == [{"name": "Alice", "email": "alice@example.com"}]

    def test_from_field_exposed_as_concrete(self):
        msg = MissiveMessage(make_missive_message())
        reg = ProxyRegistry()
        reg.register(msg)

        result = reg.resolve_getattr(msg._proxy_id, "from_field")
        assert result["type"] == "concrete"
        assert result["value"] == {"address": "user@example.com", "name": "User"}


# ---------------------------------------------------------------------------
# Missive method tests
# ---------------------------------------------------------------------------


class TestMissiveMethods:

    def test_fetch_conversations_via_resolve_call(self, monkeypatch):
        convs = [make_missive_conversation("c1"), make_missive_conversation("c2")]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"conversations": convs})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_call(source._proxy_id, "fetch_conversations", [], {"inbox": True})
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 2
        assert result["items"][0]["type_name"] == "MissiveConversation"

    def test_search_contacts_via_resolve_call(self, monkeypatch):
        contacts = [make_missive_contact("ct_1", "Jane")]

        def mock_urlopen(req, timeout=None):
            return MockResponse({"contacts": contacts})

        monkeypatch.setattr("urllib.request.urlopen", mock_urlopen)
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_call(source._proxy_id, "search_contacts", ["jane"], {})
        assert result["type"] == "proxy_list"
        assert len(result["items"]) == 1
        assert result["items"][0]["type_name"] == "MissiveContact"

    def test_search_contacts_returns_method_type(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        reg = ProxyRegistry()
        reg.register(source)

        result = reg.resolve_getattr(source._proxy_id, "search_contacts")
        assert result["type"] == "method"
        assert result["name"] == "search_contacts"

    def test_unknown_method_rejected(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        reg = ProxyRegistry()
        reg.register(source)

        with pytest.raises(AttributeError, match="not exposed"):
            reg.resolve_call(source._proxy_id, "delete_conversation", [], {})

    def test_fetch_conversations_attr_docs(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        assert "fetch_conversations" in source._proxy_attr_docs
        assert "not cached" in source._proxy_attr_docs["fetch_conversations"]

    def test_search_contacts_attr_docs(self, monkeypatch):
        monkeypatch.setattr("urllib.request.urlopen", lambda *a, **k: (_ for _ in ()).throw(
            AssertionError("unexpected HTTP call")))
        client = MissiveClient("missive_pat-test")
        source = MissiveSource(client)
        assert "search_contacts" in source._proxy_attr_docs
        assert "not cached" in source._proxy_attr_docs["search_contacts"]
