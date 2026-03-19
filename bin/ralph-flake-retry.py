#!/usr/bin/env python3
"""Notify Slack that Ralph is retrying a suspected flake.

Usage: ralph-flake-retry.py <webhook_url> <commit_subject> <retry_branch>
"""

import json
import sys
import urllib.request


def main() -> None:
    webhook_url, commit_subject, retry_branch = sys.argv[1:4]
    subject = commit_subject.split("\n")[0]
    text = f'\U0001f504 Retrying "{subject}" on {retry_branch} \u2014 suspected flake.'
    payload = json.dumps({"text": text})
    req = urllib.request.Request(
        webhook_url,
        data=payload.encode(),
        headers={"Content-Type": "application/json"},
    )
    urllib.request.urlopen(req)


if __name__ == "__main__":
    main()
