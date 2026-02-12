#!/usr/bin/env python3
"""Post commit queue results to Slack.

Usage: slack-notify-queue.py WEBHOOK_URL STATUS COMMIT_SUBJECT BRANCH RUN_URL COMMIT_URL

STATUS is "success" or "failure".
BRANCH is the queue branch name, e.g. "queue-main-philip" or
"queue-main-philip-fix_something". The third hyphen-delimited segment
is treated as the username and looked up in .github/team.json for @mentions.
"""

import json
import os
import sys
import urllib.request


def main():
    webhook_url, status, commit_msg, branch, run_url, commit_url = sys.argv[1:7]

    # First line of commit message is the subject.
    commit_subject = commit_msg.split("\n")[0]

    # Extract username: queue-main-philip-description → philip
    parts = branch.split("-")
    username = parts[2] if len(parts) >= 3 else branch

    # Load team metadata for Slack member ID lookup.
    team_file = os.path.join(os.path.dirname(__file__), "..", ".github", "team.json")
    with open(team_file) as f:
        people = json.load(f)
    alias_to_slack = {}
    for person in people:
        for alias in person.get("aliases", []):
            alias_to_slack[alias] = person["slack_member_id"]

    slack_id = alias_to_slack.get(username)
    if slack_id:
        mention = f"<@{slack_id}>"
    else:
        mention = username

    if status == "success":
        text = f"\u2705 {mention} landed: <{commit_url}|{commit_subject}>"
    else:
        text = f"\u274c {mention} failed to land: {commit_subject}\n<{run_url}|View logs>"

    payload = json.dumps({"text": text})
    req = urllib.request.Request(
        webhook_url,
        data=payload.encode(),
        headers={"Content-Type": "application/json"},
    )
    urllib.request.urlopen(req)


if __name__ == "__main__":
    main()
