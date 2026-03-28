#!/usr/bin/env python3
"""Run CI for the current commit via Buildkite. Used by the autorefine inner agent.

Pushes HEAD to a kite-test-* branch, polls Buildkite for results, prints
pass/fail status and failure logs (if any) to stdout. Cleans up the branch.

Exit code 0 = passed, 1 = failed (logs on stdout), 2 = infra error.
"""

import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

BUILDKITE_ORG = "bold-software"
BUILDKITE_PIPELINE = "exe-kite-queue"
BUILDKITE_TERMINAL_STATES = {"passed", "failed", "blocked", "canceled", "skipped", "not_run"}
CODE_REPO = "boldsoftware/exe"
FIND_TIMEOUT = 180
POLL_INTERVAL = 15
CI_TIMEOUT = 3600


def git(*args):
    r = subprocess.run(["git", *args], capture_output=True, text=True)
    if r.returncode != 0:
        print(f"git error: {r.stderr.strip()}", file=sys.stderr)
        sys.exit(2)
    return r.stdout.strip()


def bk_api(path):
    token = os.environ.get("BUILDKITE_API_TOKEN", "")
    if not token:
        print("BUILDKITE_API_TOKEN not set", file=sys.stderr)
        sys.exit(2)
    url = f"https://api.buildkite.com/v2/organizations/{BUILDKITE_ORG}/pipelines/{BUILDKITE_PIPELINE}/{path}"
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read())
    except (urllib.error.URLError, json.JSONDecodeError, OSError) as e:
        print(f"Buildkite API error: {e}", file=sys.stderr)
        return None


def main():
    sha = git("rev-parse", "HEAD")
    subject = git("--no-pager", "log", "--format=%s", "-n", "1", "HEAD")

    # Slugify subject for branch name
    slug = re.sub(r"[^a-z0-9._-]+", "_", subject.lower()).strip("_.-")[:60]
    branch = f"kite-test-bored-ci-{sha[:8]}-{slug}"

    # Push
    print(f"Pushing {sha[:7]} to {branch}...", file=sys.stderr)
    git("push", "-f", "origin", f"{sha}:refs/heads/{branch}")

    try:
        # Find build
        branch_enc = urllib.parse.quote(branch, safe="")
        deadline = time.monotonic() + FIND_TIMEOUT
        build = None
        while time.monotonic() < deadline:
            data = bk_api(f"builds?branch={branch_enc}&commit={sha}&per_page=1")
            if data and len(data) > 0:
                build = data[0]
                break
            time.sleep(3)

        if not build:
            print("No Buildkite build found", file=sys.stderr)
            sys.exit(2)

        web_url = build.get("web_url", "")
        print(f"Build: {web_url}", file=sys.stderr)

        # Poll
        build_number = build["number"]
        t0 = time.monotonic()
        state = ""
        while time.monotonic() - t0 < CI_TIMEOUT:
            data = bk_api(f"builds/{build_number}")
            state = data.get("state", "") if data else ""
            if state in BUILDKITE_TERMINAL_STATES:
                break
            elapsed = int(time.monotonic() - t0)
            m, s = divmod(elapsed, 60)
            print(f"\rCI running [{m}m{s:02d}s]", end="", file=sys.stderr)
            time.sleep(POLL_INTERVAL)
        print(file=sys.stderr)

        if state == "passed":
            print("CI passed")
            sys.exit(0)

        # Failed — fetch logs
        print(f"CI failed (state: {state})")
        if web_url:
            print(f"Build URL: {web_url}")

        build_data = bk_api(f"builds/{build_number}")
        if build_data:
            for job in build_data.get("jobs", []):
                if job.get("state") != "failed":
                    continue
                name = job.get("name", "unknown")
                log_url = job.get("raw_log_url", "")
                if not log_url:
                    continue
                token = os.environ.get("BUILDKITE_API_TOKEN", "")
                req = urllib.request.Request(log_url, headers={"Authorization": f"Bearer {token}"})
                try:
                    with urllib.request.urlopen(req, timeout=30) as resp:
                        job_log = resp.read().decode("utf-8", errors="replace")
                        lines = job_log.strip().split("\n")
                        if len(lines) > 200:
                            lines = lines[-200:]
                        print(f"\n=== FAILED JOB: {name} ===")
                        print("\n".join(lines))
                except (urllib.error.URLError, OSError) as e:
                    print(f"(could not fetch logs for {name}: {e})")

        sys.exit(1)

    finally:
        # Clean up branch
        subprocess.run(
            ["git", "push", "origin", "--delete", branch],
            capture_output=True, text=True,
        )


if __name__ == "__main__":
    main()
