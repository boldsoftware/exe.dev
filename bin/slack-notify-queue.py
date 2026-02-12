#!/usr/bin/env python3
"""Post commit queue results to Slack.

Usage: slack-notify-queue.py WEBHOOK_URL STATUS COMMIT_SUBJECT ACTOR RUN_URL COMMIT_URL BRANCH_NAME

Environment variables:
  COMMIT_LOG:  newline-separated "sha subject" lines (up to 15) from push-to-main
  COMPARE_URL: GitHub compare URL for the range of commits pushed

STATUS is "success" or "failure".
ACTOR is the GitHub username of the person who pushed the commit.
BRANCH_NAME is the queue branch name (e.g. queue-main-philip), used to
resolve the real human when the actor is a bot.
"""

import json
import os
import sys
import urllib.request


def main():
    webhook_url, status, commit_msg, actor, run_url, commit_url, branch_name = sys.argv[1:8]

    # First line of commit message is the subject.
    commit_subject = commit_msg.split("\n")[0]

    commit_log = os.environ.get("COMMIT_LOG", "").strip()
    compare_url = os.environ.get("COMPARE_URL", "").strip()

    # Load team metadata for Slack member ID lookup.
    team_file = os.path.join(os.path.dirname(__file__), "..", ".github", "team.json")
    with open(team_file) as f:
        people = json.load(f)

    # If the actor is a bot (e.g. the formatting commit triggered the run),
    # resolve the real human from the queue branch name (queue-main-<user>).
    if "[bot]" in actor:
        parts = branch_name.split("-", 2)
        if len(parts) >= 3:
            actor = parts[2]

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
        commits = [line for line in commit_log.splitlines() if line.strip()] if commit_log else []
        if len(commits) <= 1:
            # Single commit: same format as before.
            text = f"\u2705 {mention} landed: <{commit_url}|{commit_subject}>"
        else:
            link_url = compare_url if compare_url else commit_url
            subjects = []
            for line in commits:
                # Each line is "sha subject"; strip the sha prefix.
                parts = line.split(" ", 1)
                subjects.append(parts[1] if len(parts) > 1 else line)
            bullet_list = "\n".join(f"  \u2022 {s}" for s in subjects)
            text = f"\u2705 {mention} landed {len(commits)} commits: <{link_url}|compare>\n{bullet_list}"
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
