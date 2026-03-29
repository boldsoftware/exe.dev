#!/usr/bin/env python3
"""Generate the Buildkite pipeline dynamically.

Detects which files changed vs origin/main, then assembles the pipeline
from YAML segments in .buildkite/segments/::
  - commit-validation.yml  (always)
  - exe.yml                (if exe files changed) — base steps only
  - shelley.yml            (if shelley files changed)
  - format.yml             (always)
  - push.yml               (only for kite-queue-* branches)

e1e shard steps are generated dynamically based on environment variables:
  E1E_SHARDS          — number of e1e shard steps (default 5)
  E1E_VM_CONCURRENCY  — VMs per shard (default 12)
  E1E_GOMAXPROCS      — GOMAXPROCS for e1e tests (default: unset / Go default)
  E1E_EXELETS_VM_CONCURRENCY — VMs for exelets step (default 10)

The segments are plain YAML lists of steps. push.yml uses the placeholder
string __ALL_DEPS__ which is replaced with the actual dependency list at
generation time.
"""

import math
import os
import re
import subprocess
import sys

SEGMENTS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "segments")

# Letters that have e1e tests with approximate execution time weights (seconds).
# Used to balance shards by execution time rather than just test count.
# Update these when test distribution changes significantly.
# Last updated: 2026-03-29 from build #282 timing data.
TEST_LETTER_COUNTS = [
    ("A", 0), ("B", 12), ("C", 43), ("D", 8), ("E", 57), ("F", 0),
    ("G", 4), ("H", 24), ("I", 80), ("J", 0), ("K", 0), ("L", 41),
    ("M", 13), ("N", 68), ("O", 0), ("P", 88), ("Q", 0), ("R", 187),
    ("S", 147), ("T", 276), ("U", 29), ("V", 58), ("W", 2), ("X", 0),
    ("Y", 0), ("Z", 0),
]


def detect_changes():
    """Return (exe_changed, shelley_changed) by diffing against origin/main."""
    # Always fetch origin/main to ensure it's up-to-date. The CI checkout
    # only fetches the specific commit SHA, leaving origin/main stale from
    # a previous build. A stale origin/main causes the diff to include
    # unrelated files, defeating the shelley-only optimization.
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


def split_letters(n_shards):
    """Split A-Z into n_shards groups balanced by test count.

    Returns list of (start_letter, end_letter) tuples.
    Uses dynamic programming to find split points that minimize
    the maximum shard size.
    """
    letters = [l for l, _ in TEST_LETTER_COUNTS]
    counts = [c for _, c in TEST_LETTER_COUNTS]
    n = len(counts)

    # Prefix sums for range queries
    prefix = [0] * (n + 1)
    for i in range(n):
        prefix[i + 1] = prefix[i] + counts[i]

    def range_sum(i, j):
        """Sum of counts[i..j] inclusive."""
        return prefix[j + 1] - prefix[i]

    # dp[k][i] = min possible max-shard-size using k shards for letters[0..i]
    INF = float('inf')
    dp = [[INF] * n for _ in range(n_shards + 1)]
    split_at = [[0] * n for _ in range(n_shards + 1)]

    # Base: 1 shard
    for i in range(n):
        dp[1][i] = range_sum(0, i)

    # Fill DP
    for k in range(2, n_shards + 1):
        for i in range(k - 1, n):
            for j in range(k - 2, i):
                cost = max(dp[k - 1][j], range_sum(j + 1, i))
                if cost < dp[k][i]:
                    dp[k][i] = cost
                    split_at[k][i] = j

    # Backtrack to find split points
    splits = []
    k = n_shards
    i = n - 1
    while k > 1:
        j = split_at[k][i]
        splits.append(j)
        i = j
        k -= 1
    splits.reverse()

    # Convert split points to letter ranges
    shards = []
    prev = 0
    for s in splits:
        shards.append((letters[prev], letters[s]))
        prev = s + 1
    shards.append((letters[prev], letters[-1]))

    return shards


def generate_e1e_steps(n_shards, vm_concurrency, gomaxprocs, exelets_vm_concurrency):
    """Generate YAML text for e1e shard steps + exelets step."""
    shards = split_letters(n_shards)
    lines = []

    for i, (start, end) in enumerate(shards):
        shard_num = i + 1
        run_filter = f"^Test[{start}-{end}{start.lower()}-{end.lower()}]"
        label = f":rocket: e1e tests ({start}-{end})"

        lines.append(f'- label: "{label}"')
        lines.append(f'  key: test-e1e-{shard_num}')
        lines.append(f'  depends_on:')
        lines.append(f'    - build-e1e')
        lines.append(f'    - ensure-snapshot')
        lines.append(f'  command: python3 .buildkite/steps/test-e1e.py')
        lines.append(f'  timeout_in_minutes: 20')
        lines.append(f'  env:')
        lines.append(f'    VM_DRIVER: cloudhypervisor')
        lines.append(f'    E1E_SHARD: "{shard_num}"')
        lines.append(f'    E1E_RUN_FILTER: "{run_filter}"')
        lines.append(f'    E1E_VM_CONCURRENCY: "{vm_concurrency}"')
        if gomaxprocs:
            lines.append(f'    E1E_GOMAXPROCS: "{gomaxprocs}"')
        lines.append(f'  artifact_paths:')
        lines.append(f'    - "recordings-{shard_num}.html"')
        lines.append(f'    - "test-gantt-{shard_num}.html"')
        lines.append(f'    - "e1e-results-{shard_num}.json"')
        lines.append(f'    - "e1e-results-{shard_num}.xml"')
        lines.append(f'    - "e1e-logs-{shard_num}/**/*"')
        lines.append('')

    # Exelets step
    lines.append('- label: ":electric_plug: e1e exelets"')
    lines.append('  key: test-exelets')
    lines.append('  depends_on:')
    lines.append('    - build-e1e')
    lines.append('    - ensure-snapshot')
    lines.append('  command: python3 .buildkite/steps/test-e1e-exelets.py')
    lines.append('  timeout_in_minutes: 15')
    lines.append('  env:')
    lines.append('    VM_DRIVER: cloudhypervisor')
    lines.append(f'    E1E_EXELETS_VM_CONCURRENCY: "{exelets_vm_concurrency}"')
    lines.append('  artifact_paths:')
    lines.append('    - "e1e-results-exelets.json"')
    lines.append('    - "test-gantt-exelets.html"')
    lines.append('    - "e1e-results-exelets.xml"')
    lines.append('    - "e1e-logs-exelets/**/*"')

    return '\n'.join(lines)


def read_commit_trailers():
    """Read key=value trailers from the HEAD commit message.

    Trailers are lines at the end of the commit message in the form:
      Key: value
    We look for CI-specific ones: E1E-Shards, E1E-VM-Concurrency,
    E1E-GOMAXPROCS, E1E-Exelets-VM-Concurrency.
    """
    result = subprocess.run(
        ["git", "log", "-1", "--format=%B"],
        capture_output=True, text=True,
    )
    msg = result.stdout if result.returncode == 0 else ""
    trailers = {}
    for line in msg.splitlines():
        m = re.match(r'^([\w-]+):\s*(.+)$', line.strip())
        if m:
            trailers[m.group(1).lower()] = m.group(2).strip()
    return trailers


def main():
    exe_changed, shelley_changed = detect_changes()

    branch = os.environ.get("BUILDKITE_BRANCH", "")
    is_queue = branch.startswith("kite-queue-")

    # Read tuning knobs from commit trailers, then env vars, then defaults.
    trailers = read_commit_trailers()
    n_shards = int(trailers.get("e1e-shards", os.environ.get("E1E_SHARDS", "5")))
    vm_concurrency = trailers.get("e1e-vm-concurrency", os.environ.get("E1E_VM_CONCURRENCY", "12"))
    gomaxprocs = trailers.get("e1e-gomaxprocs", os.environ.get("E1E_GOMAXPROCS", ""))
    exelets_vm_concurrency = trailers.get("e1e-exelets-vm-concurrency", os.environ.get("E1E_EXELETS_VM_CONCURRENCY", "10"))

    print(f"exe_changed={exe_changed} shelley_changed={shelley_changed} "
          f"is_queue={is_queue} branch={branch} "
          f"e1e_shards={n_shards} vm_concurrency={vm_concurrency} "
          f"gomaxprocs={gomaxprocs or '(default)'} "
          f"exelets_vm_concurrency={exelets_vm_concurrency}",
          file=sys.stderr)

    # Assemble step segments
    segments = []

    # Always: commit validation
    segments.append(load_segment("commit-validation.yml"))

    # Conditional: exe tests
    if exe_changed:
        segments.append(load_segment("exe.yml"))
        # Generate e1e steps dynamically
        segments.append(generate_e1e_steps(n_shards, vm_concurrency, gomaxprocs, exelets_vm_concurrency))

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
