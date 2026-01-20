#!/usr/bin/env python3
"""Find a Shelley release matching the given tree hash.

Clones boldsoftware/shelley and uses git operations to find which tag
has the matching tree hash, then polls for that tag to appear in releases.

Usage:
    find-shelley-release.py <expected_tree_hash> [--timeout=600] [--interval=10]
    find-shelley-release.py --version=v0.89.914374232

Outputs the matching version tag to stdout on success.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request

GITHUB_API = "https://api.github.com/repos/boldsoftware/shelley"
SHELLEY_REPO = "https://github.com/boldsoftware/shelley.git"


def fetch_json(url: str) -> dict:
    """Fetch JSON from a URL."""
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def is_tag_released(tag: str) -> bool:
    """Check if a tag has a GitHub release."""
    try:
        fetch_json(f"{GITHUB_API}/releases/tags/{tag}")
        return True
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return False
        raise


def run_git(args: list[str], cwd: str) -> str:
    """Run a git command and return stdout."""
    result = subprocess.run(
        ["git"] + args,
        cwd=cwd,
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout.strip()


def find_tag_for_tree(repo_dir: str, expected_tree: str) -> str:
    """Find which tag in the repo has the given tree hash."""
    # Get all tags
    tags_output = run_git(["tag", "-l", "v*"], repo_dir)
    if not tags_output:
        return ""

    for tag in tags_output.split("\n"):
        tag = tag.strip()
        if not tag:
            continue
        try:
            tree_hash = run_git(["rev-parse", f"{tag}^{{tree}}"], repo_dir)
            if tree_hash == expected_tree:
                return tag
        except subprocess.CalledProcessError:
            continue
    return ""


def find_matching_release(expected_tree: str, timeout: int, interval: int) -> str:
    """Find the tag with matching tree hash, then wait for its release."""
    deadline = time.time() + timeout

    with tempfile.TemporaryDirectory() as tmpdir:
        repo_dir = os.path.join(tmpdir, "shelley")

        # Clone shelley repo
        print("Cloning shelley repository...", file=sys.stderr)
        subprocess.run(
            ["git", "clone", "--bare", "--filter=tree:0", SHELLEY_REPO, repo_dir],
            check=True,
            capture_output=True,
        )

        # Find which tag has the matching tree hash
        tag = find_tag_for_tree(repo_dir, expected_tree)
        if not tag:
            print(f"No tag found with tree hash {expected_tree}", file=sys.stderr)
            return ""

        print(f"Found tag {tag} with matching tree hash", file=sys.stderr)

        # Poll for the release to exist
        attempt = 0
        while time.time() < deadline:
            attempt += 1
            print(f"Attempt {attempt}: checking if {tag} is released...", file=sys.stderr)

            if is_tag_released(tag):
                print(f"Release {tag} exists!", file=sys.stderr)
                return tag

            if time.time() + interval < deadline:
                print(f"Not yet released, waiting {interval}s...", file=sys.stderr)
                time.sleep(interval)

    return ""


def main():
    parser = argparse.ArgumentParser(description="Find matching Shelley release")
    parser.add_argument("tree_hash", nargs="?", help="Expected tree hash to match")
    parser.add_argument("--version", help="Use this version directly (skip matching)")
    parser.add_argument("--timeout", type=int, default=600, help="Timeout in seconds")
    parser.add_argument("--interval", type=int, default=10, help="Poll interval in seconds")
    args = parser.parse_args()

    if args.version:
        print(args.version)
        return

    if not args.tree_hash:
        parser.error("tree_hash required unless --version is specified")

    print(f"Looking for OSS release with tree hash: {args.tree_hash}", file=sys.stderr)

    version = find_matching_release(args.tree_hash, args.timeout, args.interval)
    if not version:
        print(f"ERROR: No matching release found after {args.timeout}s", file=sys.stderr)
        print(f"Expected tree hash: {args.tree_hash}", file=sys.stderr)
        sys.exit(1)

    print(version)


if __name__ == "__main__":
    main()
