#!/usr/bin/env python3
"""Post commit queue results to Slack.

Usage: slack-notify-queue.py WEBHOOK_URL STATUS COMMIT_SUBJECT ACTOR RUN_URL COMMIT_URL BRANCH_NAME

Environment variables:
  COMMIT_LOG:     newline-separated "sha subject" lines (up to 15) from push-to-main
  COMMIT_AUTHOR:  git commit author name, used as fallback when ACTOR doesn't resolve
  CI_SOURCE:      "buildkite" or "gha" (default); adds 🪁 for buildkite runs

STATUS is "success" or "failure".
ACTOR is the GitHub username of the person who pushed the commit.
BRANCH_NAME is the queue branch name (e.g. queue-main-philip), used to
resolve the real human when the actor is a bot.
"""

import json
import os
import sys
import urllib.request


def extract_user_from_branch(branch_name):
    """Extract the username from a queue branch name.

    Branch names follow the pattern:
      queue-main-<user>-<slug>  or  queue-main-<user>
      kite-queue-<user>-<slug>  or  kite-queue-<user>
      kite-test-<user>-<slug>   or  kite-test-<user>

    The <user> is always the third hyphen-delimited segment.
    """
    parts = branch_name.split("-")
    if len(parts) >= 3:
        return parts[2]
    return None


def resolve_mention(actor, branch_name, people):
    """Resolve a Slack mention from actor, branch name, or commit author.

    Tries these sources in order:
    1. actor (GitHub username)
    2. user extracted from branch name (queue-main-<user> or kite-queue-<user>)
    3. COMMIT_AUTHOR env var (git author name)
    """
    candidates = [actor]

    branch_user = extract_user_from_branch(branch_name)
    if branch_user:
        candidates.append(branch_user)

    commit_author = os.environ.get("COMMIT_AUTHOR", "").strip()
    if commit_author:
        candidates.append(commit_author)

    for candidate in candidates:
        # Skip bot/system actors.
        if "[bot]" in candidate:
            continue
        for person in people:
            if person.get("github") == candidate or candidate in person.get("aliases", []):
                return f"<@{person['slack_member_id']}>"

    # No match found; use the best human-readable name available.
    return commit_author or actor


def main():
    webhook_url, status, commit_msg, actor, run_url, commit_url, branch_name = sys.argv[1:8]

    # First line of commit message is the subject.
    commit_subject = commit_msg.split("\n")[0]

    commit_log = os.environ.get("COMMIT_LOG", "").strip()
    ci_source = os.environ.get("CI_SOURCE", "gha").strip().lower()

    # Load team metadata for Slack member ID lookup.
    team_file = os.path.join(os.path.dirname(__file__), "..", ".github", "team.json")
    with open(team_file) as f:
        people = json.load(f)

    mention = resolve_mention(actor, branch_name, people)

    if status != "success" and "CI ONLY" in commit_subject.upper():
        return

    # Add kite emoji for Buildkite runs.
    source_tag = " \ud83e\ude81" if ci_source == "buildkite" else ""

    if status == "success":
        commits = [line for line in commit_log.splitlines() if line.strip()] if commit_log else []
        if len(commits) <= 1:
            text = f"\u2705 {mention} landed: <{commit_url}|{commit_subject}> (<{run_url}|job>){source_tag}"
        else:
            # Derive repo base URL from commit_url to build per-commit links.
            repo_url = commit_url.rsplit("/commit/", 1)[0]
            items = []
            for line in commits:
                # Each line is "sha subject"; split to get individual commit links.
                parts = line.split(" ", 1)
                sha = parts[0]
                subject = parts[1] if len(parts) > 1 else line
                items.append(f"  \u2022 <{repo_url}/commit/{sha}|{subject}>")
            bullet_list = "\n".join(items)
            text = f"\u2705 {mention} landed {len(commits)} commits: (<{run_url}|job>){source_tag}\n{bullet_list}"
    else:
        text = f"\u274c {mention} failed to land: {commit_subject}\n<{run_url}|View logs>{source_tag}"

    payload = json.dumps({"text": text})
    req = urllib.request.Request(
        webhook_url,
        data=payload.encode(),
        headers={"Content-Type": "application/json"},
    )
    urllib.request.urlopen(req)


if __name__ == "__main__":
    main()
