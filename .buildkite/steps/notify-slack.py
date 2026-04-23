#!/usr/bin/env python3
"""Post build failure results to Slack.

Runs as a pipeline step with allow_dependency_failure: true so it
executes even when tests fail.  Only sends a notification when the
build actually failed — the success path is handled by
rebase-and-push.py which has richer context (rebased SHA, commit log).

Secrets needed (via `buildkite-agent secret get`):
  NTFY_SLACK_WEBHOOK_URL
  BUILDKITE_API_TOKEN
"""

import json
import os
import re
import subprocess
import sys
import urllib.request


def bk_secret(name):
    r = subprocess.run(
        ["buildkite-agent", "secret", "get", name],
        capture_output=True, text=True,
    )
    return r.stdout.strip() if r.returncode == 0 else ""


def get_build_status(api_token, build_number):
    """Query Buildkite API for this build's jobs and return 'success' or 'failure'."""
    if not api_token:
        # No token — fall back to optimistic. The pipeline would have
        # stopped before reaching notify if something hard-failed.
        return "success"

    url = (
        f"https://api.buildkite.com/v2/organizations/bold-software"
        f"/pipelines/exe-kite-queue/builds/{build_number}"
    )
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {api_token}"})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            build = json.loads(resp.read())
    except Exception as e:
        print(f"WARNING: could not fetch build status: {e}", file=sys.stderr)
        return "success"

    for job in build.get("jobs", []):
        name = job.get("name", "")
        state = job.get("state", "")
        # Skip the notify step itself
        if "notify" in name.lower():
            continue
        if state == "failed":
            return "failure"
    return "success"


def extract_actor(branch):
    """Extract username from kite-queue-<user>-<rest> or kite-queue-<user>."""
    m = re.match(r"^kite-(?:queue|test)-([^-]+)", branch)
    return m.group(1) if m else "unknown"


def main():
    webhook_url = bk_secret("NTFY_SLACK_WEBHOOK_URL")
    if not webhook_url:
        print("ERROR: NTFY_SLACK_WEBHOOK_URL secret not available", file=sys.stderr)
        sys.exit(1)

    api_token = bk_secret("BUILDKITE_API_TOKEN")
    build_number = os.environ.get("BUILDKITE_BUILD_NUMBER", "")

    status = get_build_status(api_token, build_number)

    if status == "success":
        # Success notification is sent by rebase-and-push.py with richer
        # context (rebased commit SHA, commit log, etc.).  Skip here to
        # avoid duplicate Slack messages.
        print("Build succeeded; skipping notification (handled by push step).")
        return

    branch = os.environ.get("BUILDKITE_BRANCH", "unknown")
    actor = extract_actor(branch)
    commit_subject = os.environ.get("BUILDKITE_MESSAGE", "unknown")
    run_url = os.environ.get("BUILDKITE_BUILD_URL", "")
    commit_sha = os.environ.get("BUILDKITE_COMMIT", "")
    commit_url = f"https://github.com/boldsoftware/exe/commit/{commit_sha}"

    env = os.environ.copy()
    env["CI_SOURCE"] = "buildkite"
    # Help resolve the human when the branch-derived actor is a shared CI login.
    if os.environ.get("BUILDKITE_BUILD_AUTHOR"):
        env.setdefault("COMMIT_AUTHOR", os.environ["BUILDKITE_BUILD_AUTHOR"])
    if os.environ.get("BUILDKITE_BUILD_AUTHOR_EMAIL"):
        env.setdefault("COMMIT_AUTHOR_EMAIL", os.environ["BUILDKITE_BUILD_AUTHOR_EMAIL"])

    subprocess.run(
        [
            sys.executable, "bin/slack-notify-queue.py",
            webhook_url, status, commit_subject,
            actor, run_url, commit_url, branch,
        ],
        env=env,
        check=True,
    )


if __name__ == "__main__":
    main()
