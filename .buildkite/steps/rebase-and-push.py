#!/usr/bin/env python3
"""Rebase and push to main.

All tests have passed at this point. We:
  1. Run formatters and commit changes (if the parallel format step flagged them)
  2. Generate a GitHub App installation token
  3. Configure git to use it
  4. Sync shelley/main if it has advanced
  5. Rebase onto origin/main
  6. Dry-run push to origin/main
  7. Push to subrepos (shelley, exeuntu, oss)
  8. Push to origin/main
  9. Notify Slack
  10. Trigger exeuntu build if exeuntu/ changed
  11. Delete the queue branch
"""

import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import github_token

GITHUB_ORG = github_token.GITHUB_ORG

# Subrepo remote name -> GitHub repo name.
SUBREPOS = [("shelley", "shelley"), ("exeuntu", "exeuntu"), ("oss", "exe.dev")]


def run(*cmd, check=True, capture=False):
    print(f"+ {' '.join(cmd)}", flush=True)
    if capture:
        return subprocess.run(cmd, check=check, capture_output=True, text=True)
    return subprocess.run(cmd, check=check)


def setup_subrepo_mirrors(token: str):
    """Maintain git mirrors for subrepos in Buildkite's git-mirrors directory.

    Creates/updates bare mirrors, then registers them as alternates so that
    subsequent `git fetch <remote>` still talks to GitHub but skips
    downloading objects already present in the mirror.
    """
    mirrors_dir = os.environ.get("BUILDKITE_GIT_MIRRORS_PATH", "/data/buildkite/git-mirrors")
    alternates_file = os.path.join(
        subprocess.run(["git", "rev-parse", "--git-dir"], capture_output=True, text=True, check=True).stdout.strip(),
        "objects", "info", "alternates",
    )

    # Read existing alternates so we don't duplicate.
    existing = set()
    if os.path.exists(alternates_file):
        existing = set(open(alternates_file).read().splitlines())

    # Phase 1: ensure mirrors exist, set URLs, launch fetches in parallel.
    fetch_procs = []
    for name, repo in SUBREPOS:
        url = f"https://x-access-token:{token}@github.com/{GITHUB_ORG}/{repo}.git"
        mirror_dir = os.path.join(mirrors_dir, repo)

        if not os.path.isdir(mirror_dir):
            print(f"Creating mirror for {repo} at {mirror_dir}", flush=True)
            run("git", "clone", "--mirror", url, mirror_dir)
        else:
            subprocess.run(["git", "-C", mirror_dir, "remote", "set-url", "origin", url], capture_output=True)
            cmd = ["./bin/retry.sh", "git", "-C", mirror_dir, "fetch", "--prune", "origin"]
            print(f"+ {' '.join(cmd)} &", flush=True)
            fetch_procs.append((name, repo, subprocess.Popen(cmd)))

    # Wait for all fetches.
    for name, repo, p in fetch_procs:
        rc = p.wait()
        if rc != 0:
            print(f"ERROR: mirror fetch for {repo} failed (exit {rc})", file=sys.stderr)
            sys.exit(1)

    # Phase 2: register alternates and remotes (fast, local-only).
    for name, repo in SUBREPOS:
        url = f"https://x-access-token:{token}@github.com/{GITHUB_ORG}/{repo}.git"
        mirror_dir = os.path.join(mirrors_dir, repo)
        objects_dir = os.path.join(mirror_dir, "objects")

        if objects_dir not in existing:
            with open(alternates_file, "a") as f:
                f.write(objects_dir + "\n")
            existing.add(objects_dir)

        result = subprocess.run(["git", "remote", "set-url", name, url], capture_output=True)
        if result.returncode != 0:
            run("git", "remote", "add", name, url)


def sync_shelley(token: str) -> bool:
    """If shelley/main has advanced past origin/main, sync those commits.

    Returns True if origin/main was updated (caller should re-fetch).
    """
    print("--- :arrows_counterclockwise: Check shelley/main sync", flush=True)

    # Mirrors already set up; fetch from GitHub (fast due to alternates).
    run("./bin/retry.sh", "git", "fetch", "shelley")

    shelley_tree = run("git", "rev-parse", "shelley/main^{tree}", capture=True).stdout.strip()
    exe_shelley_tree = run("git", "ls-tree", "origin/main", "shelley", "--format", "%(objectname)", capture=True).stdout.strip()

    if shelley_tree != exe_shelley_tree:
        print("shelley/main has advanced past origin/main; syncing inline...", flush=True)
        queue_head = run("git", "rev-parse", "HEAD", capture=True).stdout.strip()
        run("git", "checkout", "--detach", "origin/main")
        run("./bin/sync-commits-from-shelley.sh", "shelley/main")
        run("./bin/retry.sh", "--retry-on", "128", "git", "push", "origin", "new-exe-commit:main")
        run("git", "tag", "-d", "new-exe-commit", check=False)
        run("./bin/retry.sh", "git", "fetch", "origin", "main")
        run("git", "checkout", "--detach", queue_head)
        print("Shelley sync complete; origin/main updated", flush=True)
        return True
    else:
        print("shelley/main in sync, nothing to do", flush=True)
        return False


def push_to_subrepos(token: str):
    """Push to subrepos (shelley, exeuntu, oss) in parallel."""
    print("--- :package: Push to subrepos", flush=True)

    # Remotes and mirrors already set up by setup_subrepo_mirrors().
    # The remote name doubles as the local directory name for all subrepos.
    # Launch all pushes, then wait for all to finish.
    procs = []
    for name, _repo in SUBREPOS:
        cmd = ["bin/push-to-subrepo.sh", "main", name, name]
        print(f"+ {' '.join(cmd)} &", flush=True)
        procs.append((name, subprocess.Popen(cmd)))
    for name, p in procs:
        rc = p.wait()
        if rc != 0:
            print(f"ERROR: push to {name} failed (exit {rc})", file=sys.stderr)
            sys.exit(1)


def trigger_exeuntu_build(token: str, origin_main_before: str):
    """Trigger exeuntu build if exeuntu/ or its workflow changed."""
    print("--- :docker: Check exeuntu trigger", flush=True)
    changed = run("git", "diff", "--name-only", origin_main_before, "HEAD", capture=True).stdout
    import re
    if re.search(r'^(exeuntu/|\.github/workflows/build-exeuntu\.yml$)', changed, re.MULTILINE):
        print("exeuntu/ changed, triggering build...", flush=True)
        subject = run("git", "log", "-1", "--pretty=%s", capture=True).stdout.strip()
        # Use GitHub API to dispatch the workflow
        r = subprocess.run(
            ["curl", "-sS", "-X", "POST",
             "-H", "Accept: application/vnd.github+json",
             "-H", f"Authorization: Bearer {token}",
             "-H", "X-GitHub-Api-Version: 2022-11-28",
             f"https://api.github.com/repos/{GITHUB_ORG}/exe/actions/workflows/build-exeuntu.yml/dispatches",
             "-d", json.dumps({"ref": "main", "inputs": {"commit_subject": subject}})],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            print(f"WARNING: Failed to trigger exeuntu build: {r.stderr}", file=sys.stderr)
        else:
            print("Exeuntu build triggered.", flush=True)
    else:
        print("No exeuntu changes, skipping.", flush=True)


def notify_slack(origin_main_before: str):
    """Send Slack notification about the landed commits."""
    print("--- :slack: Notify Slack", flush=True)

    webhook_url = run("buildkite-agent", "secret", "get", "NTFY_SLACK_WEBHOOK_URL",
                      capture=True, check=False).stdout.strip()
    if not webhook_url:
        print("WARNING: NTFY_SLACK_WEBHOOK_URL secret not available, skipping Slack notification.",
              file=sys.stderr)
        return

    main_sha = run("git", "rev-parse", "HEAD", capture=True).stdout.strip()
    commit_subject = run("git", "log", "-1", "--format=%s", capture=True).stdout.strip()
    # Use git author name for attribution (resolves to team member in the script).
    commit_author = run("git", "log", "-1", "--format=%an", capture=True).stdout.strip()
    branch = os.environ.get("BUILDKITE_BRANCH", "")
    build_url = os.environ.get("BUILDKITE_BUILD_URL", "")
    commit_url = f"https://github.com/{GITHUB_ORG}/exe/commit/{main_sha}"

    # Build commit log (same format as GHA: "sha subject" per line).
    commit_log = run(
        "git", "log", "--format=%h %s", "--reverse",
        f"{origin_main_before}..HEAD",
        capture=True,
    ).stdout.strip()

    env = os.environ.copy()
    env["COMMIT_LOG"] = commit_log
    env["COMMIT_AUTHOR"] = commit_author
    env["CI_SOURCE"] = "buildkite"

    # Extract actor from branch name (kite-queue-<user>-...).
    parts = branch.split("-")
    actor = parts[2] if len(parts) >= 3 else "buildkite"

    result = subprocess.run(
        ["python3", "bin/slack-notify-queue.py",
         webhook_url, "success", commit_subject, actor,
         build_url, commit_url, branch],
        env=env,
    )
    if result.returncode != 0:
        print("WARNING: Slack notification failed.", file=sys.stderr)
    else:
        print("Slack notification sent.", flush=True)


def delete_queue_branch(token: str):
    """Delete the kite-queue-* branch after successful merge."""
    branch = os.environ.get("BUILDKITE_BRANCH", "")
    if not branch.startswith("kite-queue-"):
        return

    print(f"--- :wastebasket: Delete queue branch {branch}", flush=True)

    # Get the SHA of the queue branch on the remote
    ls_out = run(
        "./bin/retry.sh", "git", "ls-remote", "origin", branch,
        capture=True,
    ).stdout.strip()
    queue_sha = ls_out.split()[0] if ls_out else ""

    if not queue_sha:
        print(f"Branch {branch} not found on remote, nothing to delete.", flush=True)
        return

    # Verify the branch is an ancestor of main
    run("./bin/retry.sh", "git", "fetch", "origin", "main")
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", queue_sha, "origin/main"],
        capture_output=True,
    )
    if result.returncode == 0:
        run(
            "./bin/retry.sh", "--retry-on", "128",
            "git", "push", f"--force-with-lease={branch}:{queue_sha}",
            "origin", f":refs/heads/{branch}",
            check=False,
        )
        print(f"Deleted branch {branch}", flush=True)
    else:
        print(f"Branch {branch} not yet ancestor of main, skipping delete.", flush=True)


def maybe_format():
    """Run formatters and commit if the parallel format check flagged changes."""
    needs = run(
        "buildkite-agent", "meta-data", "get", "needs_formatting",
        capture=True, check=False,
    )
    if needs.returncode != 0 or needs.stdout.strip() != "true":
        print("No formatting needed, skipping.", flush=True)
        return

    print("Formatting changes detected earlier, re-running formatters...", flush=True)
    os.environ["PATH"] = f"/usr/local/go/bin:{os.environ.get('HOME', '')}/go/bin:{os.environ.get('HOME', '')}/.local/bin:{os.environ['PATH']}"
    result = run("bash", "-c", "source .buildkite/steps/setup-shelley-deps.sh && ./bin/run_formatters.sh", check=False)
    if result.returncode != 0:
        print("ERROR: Formatting failed.", file=sys.stderr)
        sys.exit(1)

    if subprocess.run(["git", "diff", "--quiet"]).returncode != 0:
        run("git", "add", ".")
        run("git", "commit", "-m", "all: fix formatting")
    else:
        print("Formatters ran but produced no diff (race?), continuing.", flush=True)


def main():
    print("--- :art: Check formatting", flush=True)
    run("git", "config", "user.email", "ci@exe.dev")
    run("git", "config", "user.name", "exe CI")

    maybe_format()

    print("--- :key: Acquire GitHub App token", flush=True)
    token = github_token.get_cached()
    github_token.configure_origin(token)
    print("Token acquired.", flush=True)

    print("--- :git: Rebase onto origin/main", flush=True)
    run("git", "fetch", "origin", "main")

    # Set up local mirrors for subrepo remotes (reuses Buildkite git-mirrors dir).
    # Fetches still go to GitHub, but objects already in the mirror are skipped.
    setup_subrepo_mirrors(token)

    # Sync shelley/main if it has advanced
    shelley_synced = sync_shelley(token)

    # Re-fetch only if shelley sync pushed to origin/main.
    if shelley_synced:
        run("./bin/retry.sh", "git", "fetch", "origin", "main")

    head_sha = run("git", "rev-parse", "--short", "HEAD", capture=True).stdout.strip()
    main_sha = run("git", "rev-parse", "--short", "origin/main", capture=True).stdout.strip()
    origin_main_before = run("git", "rev-parse", "origin/main", capture=True).stdout.strip()
    print(f"HEAD: {head_sha}, origin/main: {main_sha}", flush=True)

    result = run("git", "rebase", "origin/main", check=False)
    if result.returncode != 0:
        run("git", "rebase", "--abort", check=False)
        print("ERROR: Rebase failed. Rebase locally and re-push.", file=sys.stderr)
        sys.exit(1)

    new_sha = run("git", "rev-parse", "--short", "HEAD", capture=True).stdout.strip()
    print(f"Rebased: {head_sha} -> {new_sha} (on {main_sha})", flush=True)

    # Dry-run push to fail fast before subrepo pushes
    print("--- :rocket: Push to origin/main", flush=True)
    result = run("./bin/retry.sh", "--retry-on", "128", "git", "push", "--dry-run", "origin", "HEAD:main", check=False)
    if result.returncode != 0:
        print("ERROR: Dry-run push failed. Someone may have pushed in the meantime.", file=sys.stderr)
        sys.exit(1)

    # Push to subrepos first (mirrors GHA ordering)
    push_to_subrepos(token)

    # Push to origin/main
    result = run("./bin/retry.sh", "--retry-on", "128", "git", "push", "origin", "HEAD:refs/heads/main", check=False)
    if result.returncode != 0:
        print("ERROR: Push failed. Someone may have pushed in the meantime.", file=sys.stderr)
        sys.exit(1)

    print("Successfully pushed to main!", flush=True)

    # Notify Slack
    notify_slack(origin_main_before)

    # Trigger exeuntu build if needed
    trigger_exeuntu_build(token, origin_main_before)

    # Clean up the queue branch
    delete_queue_branch(token)


if __name__ == "__main__":
    main()
