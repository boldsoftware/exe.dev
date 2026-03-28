#!/usr/bin/env python3
"""Generate the Buildkite pipeline dynamically.

Detects which files changed vs origin/main, then assembles the pipeline
from YAML segments in .buildkite/segments/:
  - commit-validation.yml  (always)
  - exe.yml                (if exe files changed)
  - shelley.yml            (if shelley files changed)
  - format.yml             (always)
  - push.yml               (only for kite-queue-* branches)

The segments are plain YAML lists of steps. push.yml uses the placeholder
string __ALL_DEPS__ which is replaced with the actual dependency list at
generation time.
"""

import os
import re
import subprocess
import sys

SEGMENTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "segments")


def detect_changes():
    """Return (exe_changed, shelley_changed) by diffing against origin/main."""
    subprocess.run(["git", "fetch", "origin", "main"], check=True)
    result = subprocess.run(
        ["git", "diff", "--name-only", "origin/main...HEAD"],
        capture_output=True, text=True, check=True,
    )
    files = result.stdout.strip().splitlines()

    if not files:
        print("No files changed vs origin/main, defaulting to exe tests",
              file=sys.stderr)
        return True, False

    exe_changed = False
    shelley_changed = False
    for f in files:
        if f.startswith("shelley/") or f == ".github/workflows/shelley-tests.yml":
            shelley_changed = True
        else:
            exe_changed = True

    return exe_changed, shelley_changed


def load_segment(name):
    """Load a YAML segment file and return its raw text."""
    path = os.path.join(SEGMENTS_DIR, name)
    with open(path) as f:
        return f.read()


def collect_step_keys(segment_text):
    """Extract all 'key: <value>' from YAML text."""
    return re.findall(r'^\s*key:\s*(.+)$', segment_text, re.MULTILINE)


def deps_yaml(keys, indent=4):
    """Format a list of keys as a YAML list with given indentation."""
    prefix = " " * indent
    return "\n".join(f"{prefix}- {k}" for k in keys)


def main():
    exe_changed, shelley_changed = detect_changes()

    branch = os.environ.get("BUILDKITE_BRANCH", "")
    is_queue = branch.startswith("kite-queue-")

    print(f"exe_changed={exe_changed} shelley_changed={shelley_changed} "
          f"is_queue={is_queue} branch={branch}", file=sys.stderr)

    # Assemble step segments
    segments = []

    # Always: commit validation
    segments.append(load_segment("commit-validation.yml"))

    # Conditional: exe tests
    if exe_changed:
        segments.append(load_segment("exe.yml"))

    # Conditional: shelley tests
    if shelley_changed:
        segments.append(load_segment("shelley.yml"))

    # Always: formatting
    segments.append(load_segment("format.yml"))

    # Collect all step keys for dependency lists
    all_text = "\n".join(segments)
    all_keys = collect_step_keys(all_text)

    # Queue branches only: push and notify
    if is_queue:
        push_text = load_segment("push.yml")
        push_text = push_text.replace(
            "__ALL_DEPS__", "\n" + deps_yaml(all_keys),
        )
        segments.append(push_text)

    # Emit the pipeline (stdout for upload, stderr for build log)
    lines = ["agents:", "  queue: exe-ci", "", "steps:"]
    for seg in segments:
        for line in seg.rstrip().splitlines():
            lines.append(f"  {line}")
        lines.append("")

    output = "\n".join(lines)
    print(output)
    print(output, file=sys.stderr)


if __name__ == "__main__":
    main()
