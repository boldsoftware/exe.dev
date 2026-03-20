"""Local git worktree data source for RLM agents.

Client layer (host-side):
    WorktreeClient — subprocess-based git operations and file reads.

Data loader (classic RLM — no proxy objects):
    Worktree — loads code and commits as plain Python dicts/lists,
               passed directly to the RLM signature.

The local worktree is a read-only source. All operations are safe by
construction: they read from a trusted local repository. No credentials,
no network access.
"""

import functools
import logging
import os
import subprocess

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Client layer (host-side only)
# ---------------------------------------------------------------------------


# Commits to hide from newsletter output (full or prefix SHAs).
_IGNORE_COMMITS = frozenset({
    "71852544c8cea3bcdc785c885b165ade631c5928",  # execore: improve lobby ergonomics
})


class WorktreeClient:
    """Local git worktree client using subprocess.

    Runs git commands against a specific worktree root. All operations
    are read-only. Never exposed to the sandbox.
    """

    def __init__(self, path: str | None = None):
        """Initialize with a path inside the target repo.

        Resolves the repo root via ``git rev-parse --show-toplevel``.
        If *path* is None, uses the directory of this source file
        (which lives inside the repo).
        """
        if path is None:
            path = os.path.dirname(os.path.abspath(__file__))

        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            cwd=path,
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode != 0:
            raise ValueError(f"Not a git repository: {path}")

        self._root = result.stdout.strip()

    @property
    def root(self) -> str:
        return self._root

    def read_file(self, path: str) -> str:
        """Read a file from the worktree, validating it's within the repo root."""
        if os.path.isabs(path):
            resolved = os.path.realpath(path)
        else:
            resolved = os.path.realpath(os.path.join(self._root, path))

        root_real = os.path.realpath(self._root)
        if not resolved.startswith(root_real + os.sep) and resolved != root_real:
            raise ValueError(f"Path escapes repository root: {path}")

        if not os.path.isfile(resolved):
            raise FileNotFoundError(f"File not found: {path}")

        with open(resolved, "r", errors="replace") as f:
            return f.read()

    def ls_files(self) -> list[str]:
        """Return all tracked file paths."""
        result = subprocess.run(
            ["git", "ls-files"],
            cwd=self._root,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            raise RuntimeError(f"git ls-files failed: {result.stderr.strip()}")
        output = result.stdout.strip()
        return output.split("\n") if output else []

    def log(self) -> list[dict]:
        """Return all commits as structured dicts."""
        result = subprocess.run(
            ["git", "log",
             "--format=%H%x00%h%x00%s%x00%an%x00%aI%x00%B%x01"],
            cwd=self._root,
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            raise RuntimeError(f"git log failed: {result.stderr.strip()}")

        commits = []
        for entry in result.stdout.split("\x01"):
            entry = entry.strip()
            if not entry:
                continue
            parts = entry.split("\x00", 5)
            if len(parts) >= 6:
                sha = parts[0]
                if sha in _IGNORE_COMMITS:
                    continue
                commits.append({
                    "sha": sha,
                    "short": parts[1],
                    "subject": parts[2],
                    "author": parts[3],
                    "date": parts[4],
                    "body": parts[5].strip(),
                })

        return commits

    def commit_diff(self, sha: str) -> str:
        """Return the patch for a single commit."""
        if sha in _IGNORE_COMMITS:
            raise ValueError(f"commit {sha[:12]} not found")
        result = subprocess.run(
            ["git", "diff-tree", "-p", "--stat", "-r", sha],
            cwd=self._root,
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            raise RuntimeError(f"git diff-tree failed: {result.stderr.strip()}")
        return result.stdout


# ---------------------------------------------------------------------------
# Data loader (classic RLM — no proxy objects)
# ---------------------------------------------------------------------------


class Worktree:
    """Worktree data loader for RLM scripts.

    Loads all tracked files and commit history as plain Python data,
    passed directly to the RLM signature as input fields.

    Usage::

        wt = Worktree("/path/inside/repo")
        sig_fields["code"] = dspy.InputField(desc=CODE_DESC)
        sig_fields["commits"] = dspy.InputField(desc=COMMITS_DESC)
        call_kwargs["code"] = wt.code
        call_kwargs["commits"] = wt.commits
        tools.append(wt.commit_diff)
    """

    def __init__(self, path: str | None = None):
        self._client = WorktreeClient(path)

    @property
    def root(self) -> str:
        return self._client.root

    @functools.cached_property
    def code(self) -> dict[str, str]:
        """All tracked files as ``{path: content}`` dict.

        The agent searches this directly in Python::

            [p for p in code if p.endswith('.go')]
            [p for p, c in code.items() if 'migration' in c]
        """
        result = {}
        for p in self._client.ls_files():
            try:
                result[p] = self._client.read_file(p)
            except (FileNotFoundError, ValueError):
                continue
        return result

    @functools.cached_property
    def commits(self) -> list[dict]:
        """All commits (newest first) as dicts.

        Keys: sha, short, subject, author, date (ISO 8601), body.
        Use ``commit_diff(sha)`` for patches.
        """
        return self._client.log()

    def commit_diff(self, sha: str) -> str:
        """Return the unified diff (stat + patch) for a single commit."""
        return self._client.commit_diff(sha)
