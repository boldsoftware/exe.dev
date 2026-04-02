#!/usr/bin/env python3
"""Prune git worktrees whose HEAD commit subject already exists in origin/main."""

import argparse
import subprocess
import sys


def run(cmd, **kwargs):
    return subprocess.run(cmd, capture_output=True, text=True, **kwargs).stdout.strip()


def is_dirty(worktree_path):
    out = run(["git", "-C", worktree_path, "status", "--porcelain"])
    return bool(out)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "-n", "--dry-run", action="store_true",
        help="Show what would be pruned without actually doing it",
    )
    parser.add_argument(
        "--force", action="store_true",
        help="Remove even dirty worktrees",
    )
    args = parser.parse_args()

    # Find the main worktree (first entry from `git worktree list`).
    lines = run(["git", "worktree", "list", "--porcelain"]).split("\n")
    worktrees = []  # (path, head)
    cur_path = cur_head = None
    for line in lines:
        if line.startswith("worktree "):
            cur_path = line[len("worktree "):]
        elif line.startswith("HEAD "):
            cur_head = line[len("HEAD "):]
        elif line == "":
            if cur_path and cur_head:
                worktrees.append((cur_path, cur_head))
            cur_path = cur_head = None
    if cur_path and cur_head:
        worktrees.append((cur_path, cur_head))

    if not worktrees:
        print("No worktrees found.")
        return

    main_worktree = worktrees[0][0]

    # Fetch latest origin/main subjects.
    run(["git", "-C", main_worktree, "fetch", "origin", "main", "--quiet"])

    # Build set of commit subjects on origin/main.
    main_subjects = set(
        run(["git", "-C", main_worktree, "log", "--format=%s", "origin/main"]).split("\n")
    )

    pruned = 0
    skipped_dirty = 0

    for path, head in worktrees[1:]:  # skip the main worktree
        subject = run(["git", "-C", main_worktree, "log", "-1", "--format=%s", head])
        if not subject:
            continue

        if subject not in main_subjects:
            continue

        dirty = is_dirty(path)
        dirty_marker = " [dirty]" if dirty else ""
        name = path.rsplit("/", 1)[-1]

        if args.dry_run:
            print(f"{'would skip' if dirty and not args.force else 'would prune':>12}  {name}{dirty_marker}")
            print(f"{'':>12}  subject: {subject}")
            if dirty and not args.force:
                skipped_dirty += 1
            else:
                pruned += 1
            continue

        if dirty and not args.force:
            print(f"{'skip':>12}  {name}{dirty_marker}")
            print(f"{'':>12}  subject: {subject}")
            skipped_dirty += 1
            continue

        # Get branch name before removing.
        branch = run(["git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD"])

        print(f"{'prune':>12}  {name}{dirty_marker}")
        print(f"{'':>12}  subject: {subject}")
        subprocess.run(
            ["git", "-C", main_worktree, "worktree", "remove", "--force", path],
            check=True,
        )
        # Delete the branch if it still exists.
        if branch and branch != "HEAD":
            subprocess.run(
                ["git", "-C", main_worktree, "branch", "-D", branch],
                capture_output=True,
            )
        pruned += 1

    action = "would prune" if args.dry_run else "pruned"
    print(f"\n{action} {pruned} worktree(s), skipped {skipped_dirty} dirty worktree(s)")


if __name__ == "__main__":
    main()
