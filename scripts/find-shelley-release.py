#!/usr/bin/env python3
"""Find a Shelley release matching the given tree hash.

Polls the boldsoftware/shelley GitHub releases until finding one whose
root tree hash matches the expected hash. Used to coordinate exeuntu
builds with the OSS Shelley repo.

Usage:
    find-shelley-release.py <expected_tree_hash> [--timeout=600] [--interval=10]
    find-shelley-release.py --version=v0.89.914374232

Outputs the matching version tag to stdout on success.
"""

import argparse
import json
import sys
import time
import urllib.request

GITHUB_API = "https://api.github.com/repos/boldsoftware/shelley"


def fetch_json(url: str) -> dict:
    """Fetch JSON from a URL."""
    req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


def get_release_tree_hash(tag: str) -> str:
    """Get the root tree hash for a release tag."""
    # Get the tag ref
    tag_ref = fetch_json(f"{GITHUB_API}/git/refs/tags/{tag}")
    obj_sha = tag_ref["object"]["sha"]
    obj_type = tag_ref["object"]["type"]

    # If annotated tag, dereference to get commit
    if obj_type == "tag":
        tag_obj = fetch_json(f"{GITHUB_API}/git/tags/{obj_sha}")
        commit_sha = tag_obj["object"]["sha"]
    else:
        commit_sha = obj_sha

    # Get the tree hash from the commit
    commit = fetch_json(f"{GITHUB_API}/git/commits/{commit_sha}")
    return commit["tree"]["sha"]


def find_matching_release(expected_tree: str, timeout: int, interval: int) -> str:
    """Poll releases until finding one with matching tree hash."""
    deadline = time.time() + timeout
    attempt = 0

    while time.time() < deadline:
        attempt += 1
        print(f"Attempt {attempt}: checking releases...", file=sys.stderr)

        releases = fetch_json(f"{GITHUB_API}/releases?per_page=5")

        for release in releases:
            tag = release["tag_name"]
            try:
                tree_hash = get_release_tree_hash(tag)
                if tree_hash == expected_tree:
                    print(f"Found matching release: {tag} (tree: {tree_hash})", file=sys.stderr)
                    return tag
                print(f"  {tag}: tree={tree_hash} (no match)", file=sys.stderr)
            except Exception as e:
                print(f"  {tag}: error fetching tree hash: {e}", file=sys.stderr)

        if time.time() + interval < deadline:
            print(f"No match yet, waiting {interval}s...", file=sys.stderr)
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
