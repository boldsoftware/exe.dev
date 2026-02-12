#!/usr/bin/env python3
"""Post commit queue results to Slack.

Usage: slack-notify-queue.py WEBHOOK_URL STATUS COMMIT_SUBJECT ACTOR RUN_URL COMMIT_URL

STATUS is "success" or "failure".
ACTOR is the GitHub username of the person who pushed the commit.
"""

import json
import os
import sys
import urllib.request


def main():
    webhook_url, status, commit_msg, actor, run_url, commit_url = sys.argv[1:7]

    # First line of commit message is the subject.
    commit_subject = commit_msg.split("\n")[0]

    # Load team metadata for Slack member ID lookup.
    team_file = os.path.join(os.path.dirname(__file__), "..", ".github", "team.json")
    with open(team_file) as f:
        people = json.load(f)

    slack_id = None
    for person in people:
        if person.get("github") == actor or actor in person.get("aliases", []):
            slack_id = person["slack_member_id"]
            break
    if slack_id:
        mention = f"<@{slack_id}>"
    else:
        mention = actor

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
