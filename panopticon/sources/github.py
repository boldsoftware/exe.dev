"""GitHub data source: HTTP client + domain objects.

Client layer (host-side, never exposed to sandbox):
    GitHubClient — urllib.request-based GitHub REST API client with
    pagination, rate-limit tracking, and 429 retry.

Proxy objects (exposed to sandbox via allowlist):
    GitHubRepo    — root entry point: issues (including PRs), comments,
                    search_issues(), fetch_issues()
    GitHubIssue   — single issue or pull request with metadata + lazy comments
    GitHubComment — single comment with metadata

The proxy API mirrors GitHub's REST API ontology: repos contain issues
(GitHub's issues endpoint returns both issues and PRs), issues contain
comments, and search is a first-class operation. See proxy_api_design.md
for the design philosophy.
"""

import json
import logging
import time
import urllib.error
import urllib.request
from urllib.parse import parse_qs, urlencode, urlparse

from panopticon.proxy import ProxyObject

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Client layer (host-side only)
# ---------------------------------------------------------------------------

_API_BASE = "https://api.github.com"


class GitHubClient:
    """GitHub REST API client using urllib.request.

    Handles pagination (Link header), rate-limit warnings, and 429 retry.
    Never exposed to the sandbox — stored in _client attrs on domain objects.
    """

    def __init__(self, token: str):
        token = (token or "").strip()
        if not token:
            raise ValueError("EXE_GITHUB_TOKEN must be set")
        self._token = token

    def _request(self, path: str, params: dict | None = None) -> tuple[dict | list, dict]:
        """Make an authenticated GET request. Returns (parsed_json, response_headers)."""
        url = f"{_API_BASE}{path}"
        if params:
            filtered = {k: str(v) for k, v in params.items() if v is not None}
            if filtered:
                url = f"{url}?{urlencode(filtered)}"

        req = urllib.request.Request(
            url,
            headers={
                "Authorization": f"token {self._token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
            },
        )

        for attempt in range(2):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    headers = {k.lower(): v for k, v in resp.getheaders()}
                    data = json.loads(resp.read())
                    self._check_rate_limit(headers)
                    return data, headers
            except urllib.error.HTTPError as exc:
                if exc.code == 429 and attempt == 0:
                    retry_after = int(exc.headers.get("Retry-After", "5"))
                    log.warning("GitHub 429 — retrying after %ds", retry_after)
                    time.sleep(retry_after)
                    continue
                raise

        raise RuntimeError("unreachable")  # pragma: no cover

    def _check_rate_limit(self, headers: dict):
        remaining = headers.get("x-ratelimit-remaining")
        if remaining is not None and int(remaining) < 100:
            reset_ts = int(headers.get("x-ratelimit-reset", "0"))
            reset_in = max(0, reset_ts - int(time.time()))
            log.warning(
                "GitHub rate limit low: %s remaining, resets in %ds",
                remaining, reset_in,
            )

    def _paginate(self, path: str, params: dict | None = None, max_pages: int = 5) -> list:
        """Fetch up to max_pages of results, following Link: <...>; rel=\"next\"."""
        params = dict(params or {})
        all_items: list = []

        for _ in range(max_pages):
            data, headers = self._request(path, params)
            if isinstance(data, list):
                all_items.extend(data)
            else:
                all_items.append(data)

            # Parse Link header for next page
            link = headers.get("link", "")
            next_url = None
            for part in link.split(","):
                if 'rel="next"' in part:
                    next_url = part.split(";")[0].strip().strip("<>")
                    break

            if not next_url:
                break

            # For subsequent pages, parse the full URL
            parsed = urlparse(next_url)
            path = parsed.path
            params = {k: v[0] for k, v in parse_qs(parsed.query).items()} if parsed.query else {}

        return all_items

    def list_issues(
        self,
        owner: str,
        repo: str,
        state: str = "all",
        sort: str = "updated",
        direction: str = "desc",
        per_page: int = 100,
        max_pages: int = 5,
        since: str | None = None,
        labels: str | None = None,
    ) -> list[dict]:
        """List issues for a repo, sorted by updated_at desc by default."""
        params = {
            "state": state,
            "sort": sort,
            "direction": direction,
            "per_page": str(per_page),
        }
        if since is not None:
            params["since"] = since
        if labels is not None:
            params["labels"] = labels
        return self._paginate(
            f"/repos/{owner}/{repo}/issues",
            params=params,
            max_pages=max_pages,
        )

    def list_repo_comments(
        self,
        owner: str,
        repo: str,
        sort: str = "updated",
        direction: str = "desc",
        per_page: int = 100,
        max_pages: int = 3,
    ) -> list[dict]:
        """List all issue comments across a repo, sorted by updated_at desc."""
        return self._paginate(
            f"/repos/{owner}/{repo}/issues/comments",
            params={
                "sort": sort,
                "direction": direction,
                "per_page": str(per_page),
            },
            max_pages=max_pages,
        )

    def list_issue_comments(
        self,
        owner: str,
        repo: str,
        issue_number: int,
        per_page: int = 100,
        max_pages: int = 5,
    ) -> list[dict]:
        """List comments on a specific issue."""
        return self._paginate(
            f"/repos/{owner}/{repo}/issues/{issue_number}/comments",
            params={"per_page": str(per_page)},
            max_pages=max_pages,
        )

    def search_issues(
        self,
        owner: str,
        repo: str,
        q: str,
        per_page: int = 100,
        max_pages: int = 3,
    ) -> list[dict]:
        """Search issues via GET /search/issues. Scopes query to repo automatically."""
        full_q = f"repo:{owner}/{repo} {q}"
        all_items: list[dict] = []
        params: dict = {"q": full_q, "per_page": str(per_page)}

        for page in range(1, max_pages + 1):
            params["page"] = str(page)
            data, headers = self._request("/search/issues", params)
            items = data.get("items", [])
            all_items.extend(items)
            if len(all_items) >= data.get("total_count", 0):
                break
            if len(items) < per_page:
                break

        return all_items

    def list_pull_requests(
        self,
        owner: str,
        repo: str,
        state: str = "all",
        sort: str = "updated",
        direction: str = "desc",
        per_page: int = 100,
        max_pages: int = 5,
    ) -> list[dict]:
        """List pull requests for a repo, sorted by updated_at desc by default."""
        return self._paginate(
            f"/repos/{owner}/{repo}/pulls",
            params={
                "state": state,
                "sort": sort,
                "direction": direction,
                "per_page": str(per_page),
            },
            max_pages=max_pages,
        )


# ---------------------------------------------------------------------------
# Proxy objects (sandbox-visible)
# ---------------------------------------------------------------------------


def _extract_reactions(raw: dict) -> dict:
    """Extract non-zero reaction counts from GitHub's reactions object."""
    reactions = raw.get("reactions", {})
    keys = ["+1", "-1", "laugh", "hooray", "confused", "heart", "rocket", "eyes"]
    return {k: reactions[k] for k in keys if reactions.get(k, 0) > 0}


class GitHubComment(ProxyObject):
    """A single GitHub issue comment."""

    def __init__(self, raw: dict):
        author = raw.get("user", {}).get("login", "unknown")
        created = raw.get("created_at", "")
        # issue_url looks like .../repos/owner/repo/issues/123
        issue_url = raw.get("issue_url", "")
        issue_number = int(issue_url.rsplit("/", 1)[-1]) if issue_url else 0

        super().__init__(
            proxy_id=f"gh_comment_{raw['id']}",
            type_name="GitHubComment",
            doc=f"Comment by {author} on issue #{issue_number} ({created}). "
                "Access .body for the comment text, .reactions for emoji counts.",
            dir_attrs=["author", "user", "body", "created_at", "updated_at",
                        "issue_number", "url", "reactions"],
            attr_docs={
                "author": "GitHub username of the commenter",
                "user": "Alias for author (GitHub username string)",
                "body": "Comment text (markdown)",
                "created_at": "ISO 8601 creation timestamp",
                "updated_at": "ISO 8601 last-update timestamp",
                "issue_number": "Issue number this comment belongs to (int)",
                "url": "Web URL for this comment",
                "reactions": "Dict of reaction emoji to count, e.g. {'+1': 3, 'heart': 1}. Only non-zero.",
            },
        )
        self.author = author
        self.user = author  # alias — GitHub API calls this "user"
        self.body = raw.get("body", "")
        self.created_at = created
        self.updated_at = raw.get("updated_at", "")
        self.issue_number = issue_number
        self.url = raw.get("html_url", "")
        self.reactions = _extract_reactions(raw)


class GitHubIssue(ProxyObject):
    """A single GitHub issue (or pull request)."""

    def __init__(self, client: "GitHubClient", owner: str, repo: str, raw: dict):
        number = raw.get("number", 0)
        title = raw.get("title", "")

        super().__init__(
            proxy_id=f"gh_issue_{owner}_{repo}_{number}",
            type_name="GitHubIssue",
            doc=f"GitHub issue #{number}: {title}\n"
                f"State: {raw.get('state', '?')} | "
                f"Comments: {raw.get('comments', 0)} | "
                f"Author: {raw.get('user', {}).get('login', '?')}\n"
                "Access .author for username, .body for full text, .comments for "
                "discussion, .reactions for emoji counts.",
            dir_attrs=["number", "title", "body", "state", "author", "user",
                        "labels", "created_at", "updated_at", "comment_count",
                        "url", "reactions", "comments", "is_pull_request"],
            attr_docs={
                "number": "Issue number (int)",
                "title": "Issue title",
                "body": "Issue body text (markdown)",
                "state": "'open' or 'closed'",
                "author": "GitHub username of the issue author",
                "user": "Alias for author (GitHub username string)",
                "labels": "List of strings (e.g. ['bug', 'urgent']), not objects",
                "created_at": "ISO 8601 creation timestamp",
                "updated_at": "ISO 8601 last-update timestamp",
                "comment_count": "Number of comments (int)",
                "url": "Web URL for this issue",
                "reactions": "Dict of reaction emoji to count, e.g. {'+1': 3}. Only non-zero.",
                "comments": "List of GitHubComment objects for this issue (lazy-loaded, 1h TTL)",
                "is_pull_request": "True if this is a pull request, False if a regular issue",
            },
        )
        self._client = client
        self._owner = owner
        self._repo = repo
        self.number = number
        self.title = title
        self.body = raw.get("body", "") or ""
        self.state = raw.get("state", "")
        self.author = raw.get("user", {}).get("login", "unknown")
        self.user = self.author  # alias — GitHub API calls this "user"
        self.labels = [lbl["name"] for lbl in raw.get("labels", [])]
        self.created_at = raw.get("created_at", "")
        self.updated_at = raw.get("updated_at", "")
        self.comment_count = raw.get("comments", 0)
        self.url = raw.get("html_url", "")
        self.reactions = _extract_reactions(raw)
        self.is_pull_request = "pull_request" in raw
        self._comments = None
        self._comments_fetched_at = 0.0

    @property
    def comments(self):
        """Lazy-load comments for this issue. Cached with 1-hour TTL."""
        now = time.monotonic()
        if self._comments is None or (now - self._comments_fetched_at) > 3600:
            raw = self._client.list_issue_comments(self._owner, self._repo, self.number)
            self._comments = [GitHubComment(c) for c in raw]
            self._comments_fetched_at = now
        return self._comments


class GitHubPullRequest(ProxyObject):
    """A single GitHub pull request with PR-specific metadata."""

    def __init__(self, client: "GitHubClient", owner: str, repo: str, raw: dict):
        number = raw.get("number", 0)
        title = raw.get("title", "")

        super().__init__(
            proxy_id=f"gh_pr_{owner}_{repo}_{number}",
            type_name="GitHubPullRequest",
            doc=f"GitHub PR #{number}: {title}\n"
                f"State: {raw.get('state', '?')} | "
                f"Author: {raw.get('user', {}).get('login', '?')} | "
                f"Draft: {raw.get('draft', False)}\n"
                f"{raw.get('head', {}).get('ref', '?')} \u2192 {raw.get('base', {}).get('ref', '?')}\n"
                "Access .author for username, .body for full text. .comments returns\n"
                "issue-style comments only (not PR review comments).",
            dir_attrs=["number", "title", "body", "state", "author", "user",
                        "labels", "created_at", "updated_at", "merged_at",
                        "url", "head_branch", "base_branch", "draft",
                        "reactions", "comments", "comment_count"],
            attr_docs={
                "number": "PR number (int)",
                "title": "PR title",
                "body": "PR body text (markdown)",
                "state": "'open' or 'closed'",
                "author": "GitHub username of the PR author",
                "user": "Alias for author (GitHub username string)",
                "labels": "List of strings (e.g. ['bug', 'urgent']), not objects",
                "created_at": "ISO 8601 creation timestamp",
                "updated_at": "ISO 8601 last-update timestamp",
                "merged_at": "ISO 8601 merge timestamp, or empty string if not merged",
                "url": "Web URL for this pull request",
                "head_branch": "Source branch name (e.g. 'feature-xyz')",
                "base_branch": "Target branch name (e.g. 'main')",
                "draft": "True if this is a draft PR",
                "reactions": "Dict of reaction emoji to count, e.g. {'+1': 3}. Only non-zero.",
                "comments": "Issue-style comments only (not PR review comments). Lazy-loaded, 1h TTL.",
                "comment_count": "Number of issue-style comments (int)",
            },
        )
        self._client = client
        self._owner = owner
        self._repo = repo
        self.number = number
        self.title = title
        self.body = raw.get("body", "") or ""
        self.state = raw.get("state", "")
        self.author = raw.get("user", {}).get("login", "unknown")
        self.user = self.author  # alias — GitHub API calls this "user"
        self.labels = [lbl["name"] for lbl in raw.get("labels", [])]
        self.created_at = raw.get("created_at", "")
        self.updated_at = raw.get("updated_at", "")
        self.merged_at = raw.get("merged_at", "") or ""
        self.url = raw.get("html_url", "")
        self.head_branch = raw.get("head", {}).get("ref", "")
        self.base_branch = raw.get("base", {}).get("ref", "")
        self.draft = raw.get("draft", False)
        self.reactions = _extract_reactions(raw)
        self.comment_count = raw.get("comments", 0)
        self._comments = None
        self._comments_fetched_at = 0.0

    @property
    def comments(self):
        """Lazy-load comments for this PR. Cached with 1-hour TTL."""
        now = time.monotonic()
        if self._comments is None or (now - self._comments_fetched_at) > 3600:
            raw = self._client.list_issue_comments(self._owner, self._repo, self.number)
            self._comments = [GitHubComment(c) for c in raw]
            self._comments_fetched_at = now
        return self._comments


class GitHubRepo(ProxyObject):
    """Root entry point for a GitHub repository."""

    def __init__(self, client: "GitHubClient", owner: str, repo: str, description: str = ""):
        super().__init__(
            proxy_id=f"gh_repo_{owner}_{repo}",
            type_name="GitHubRepo",
            doc=f"GitHub repository {owner}/{repo}.\n\n"
                "Mirrors GitHub's REST API. .issues returns issues and PRs (matching\n"
                "the /repos/:owner/:repo/issues endpoint; use .is_pull_request to\n"
                "distinguish). .pull_requests uses /pulls for PR-specific fields\n"
                "(head/base branch, draft, merged_at). .comments returns all issue\n"
                "comments repo-wide.\n\n"
                ".search_issues(q) wraps /search/issues, auto-scoped to this repo.\n"
                ".fetch_issues(...) does server-side filtering (state, since, labels, sort).",
            dir_attrs=["name", "description", "issues", "pull_requests",
                        "comments", "search_issues", "fetch_issues"],
            attr_docs={
                "name": "Repository name in owner/repo format",
                "description": "Repository description",
                "issues": "All issues and PRs (open + closed), updated_at desc. "
                          "Mirrors /repos/:owner/:repo/issues — use .is_pull_request "
                          "to distinguish.",
                "pull_requests": "All PRs (open + closed), updated_at desc. "
                    "Mirrors /repos/:owner/:repo/pulls — includes head_branch, "
                    "base_branch, draft, merged_at.",
                "comments": "All issue comments repo-wide, updated_at desc. "
                    "Separate from .issues — read both for full coverage.",
                "search_issues": "search_issues(q) — Wraps /search/issues, auto-scoped "
                    "to this repo. Supports GitHub search qualifiers. "
                    "Hits the API on every call (not cached). Store the result in a "
                    "variable if you need it more than once.",
                "fetch_issues": "fetch_issues(state=None, since=None, labels=None, "
                    "sort=None, direction=None) — Server-side filtered issue listing "
                    "via the issues endpoint. "
                    "Hits the API on every call (not cached). Store the result in a "
                    "variable if you need it more than once.",
            },
            methods={
                "search_issues": self._search_issues,
                "fetch_issues": self._fetch_issues,
            },
        )
        self._client = client
        self._owner = owner
        self._repo = repo
        self.name = f"{owner}/{repo}"
        self.description = description
        self._issue_map: dict[int, GitHubIssue] = {}
        self._issues = None
        self._issues_fetched_at = 0.0
        self._pull_requests = None
        self._pull_requests_fetched_at = 0.0
        self._comments = None
        self._comments_fetched_at = 0.0

    def _get_or_create_issue(self, raw: dict) -> GitHubIssue:
        """Return existing GitHubIssue if already cached, otherwise create and cache."""
        number = raw.get("number", 0)
        if number in self._issue_map:
            return self._issue_map[number]
        issue = GitHubIssue(self._client, self._owner, self._repo, raw)
        self._issue_map[number] = issue
        return issue

    @property
    def issues(self):
        """All issues, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._issues is None or (now - self._issues_fetched_at) > 3600:
            raw = self._client.list_issues(self._owner, self._repo)
            # Reset identity map so search/fetch results from the previous
            # TTL window don't return stale objects.
            self._issue_map = {}
            self._issues = [self._get_or_create_issue(i) for i in raw]
            self._issues_fetched_at = now
        return self._issues

    @property
    def comments(self):
        """All repo-wide comments, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._comments is None or (now - self._comments_fetched_at) > 3600:
            raw = self._client.list_repo_comments(self._owner, self._repo)
            self._comments = [GitHubComment(c) for c in raw]
            self._comments_fetched_at = now
        return self._comments

    @property
    def pull_requests(self):
        """All pull requests, cached with 1-hour TTL."""
        now = time.monotonic()
        if self._pull_requests is None or (now - self._pull_requests_fetched_at) > 3600:
            raw = self._client.list_pull_requests(self._owner, self._repo)
            self._pull_requests = [
                GitHubPullRequest(self._client, self._owner, self._repo, pr)
                for pr in raw
            ]
            self._pull_requests_fetched_at = now
        return self._pull_requests

    def _search_issues(self, q: str) -> list:
        """Host-side search implementation. Called via resolve_call."""
        raw = self._client.search_issues(self._owner, self._repo, str(q))
        return [self._get_or_create_issue(i) for i in raw]

    def _fetch_issues(self, state=None, since=None, labels=None, sort=None, direction=None):
        """Host-side fetch_issues implementation. Passes filters to GitHub's list issues API."""
        kwargs: dict = {}
        if state is not None:
            kwargs["state"] = str(state)
        if sort is not None:
            kwargs["sort"] = str(sort)
        if direction is not None:
            kwargs["direction"] = str(direction)
        raw = self._client.list_issues(
            self._owner, self._repo,
            since=str(since) if since is not None else None,
            labels=str(labels) if labels is not None else None,
            **kwargs,
        )
        return [self._get_or_create_issue(i) for i in raw]
