#!/usr/bin/env python3
"""Generate the Buildkite pipeline dynamically.

Detects which files changed vs origin/main, then assembles the pipeline
from YAML segments in .buildkite/segments/::
  - commit-validation.yml  (always)
  - exe.yml                (if exe files changed) — base steps only
  - shelley.yml            (if shelley files changed)
  - blog.yml               (if blog/ or cmd/blogd/ files changed)
  - ui.yml                 (if ui/ files changed)
  - format.yml             (always)
  - push.yml               (only for kite-queue-* branches)

Changes that only affect observability/ are treated as no-op for exe tests.

e1e shard steps are generated dynamically based on environment variables:
  E1E_SHARDS          — number of e1e shard steps (default 5)
  E1E_VM_CONCURRENCY  — VMs per shard (default 12)
  E1E_GOMAXPROCS      — GOMAXPROCS for e1e tests (default: unset / Go default)
  E1E_EXELETS_VM_CONCURRENCY — VMs for exelets step (default 10)
  E1E_MIGRATION_SHARDS       — number of exelets-migration shards (default 4)
  E1E_EXELETS_SHARDS         — number of exelets shards (default 2)

Coverage mode (commit trailer "Coverage: true" or env E1E_COVERAGE=true):
  Builds exed/exeprox with -cover, collects coverage from all test steps,
  and adds a final merge-coverage step that produces a combined report.

Bypass mode (commit trailer "Bypass-CI: true"):
  Skips exe/shelley/blog/ui test segments. commit-validation, format,
  and push still run. Ignored on main — main always runs the full suite.

The segments are plain YAML lists of steps. push.yml uses the placeholder
string __ALL_DEPS__ which is replaced with the actual dependency list at
generation time.
"""

import math
import os
import re
import subprocess
import sys
import time

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

# e1e/exelets migration tests (TestDirectMigration*) with approximate
# execution time weights (seconds). Used to shard the migration step.
# Update when tests are added/removed or timing changes significantly.
# Last updated: 2026-04-18 from build #1183 timing data, after splitting
# TestDirectMigration into Cold + Live subtests.
# Note: splitting TestDirectMigration into Cold+Live duplicates per-test setup
# (register user + makeBox + exed Restart, ~10s) — that's the tradeoff for being
# able to run them in separate shards. Weights below include that setup cost.
# Numbers are observed wall-clock from build #1184 after the Cold/Live split.
MIGRATION_TEST_COSTS = [
    ("TestDirectMigrationCold", 31),
    ("TestDirectMigrationLive", 51),
    ("TestDirectMigrationOperatorSSHCold", 32),
    ("TestDirectMigrationOperatorSSHLive", 55),
    ("TestDirectMigrationOrphanedDataset", 30),
    ("TestDirectMigrationReconnect", 25),
    ("TestDirectMigrationResumable", 34),
    ("TestDirectMigrationResumablePhase2", 24),
]

# Heavy non-parallel tests in e1e/exelets. These tests all mutate the
# package-global `serverEnv` (calling exed.Restart with different exelet
# sets) so they cannot use t.Parallel() -- but they CAN run in separate
# Buildkite shards. We split them across N shards by LPT-greedy on cost.
# Shard 1 also runs everything else (testinfra package + the parallel
# TestEmail/TestMetadata in e1e/exelets); those are <6s each and overlap
# with the sequential block on shard 1, so they don't extend its wall.
# Last updated: 2026-04-29 from build #1971 timing data.
EXELETS_HEAVY_TEST_COSTS = [
    ("TestExedStartsWithDownHost", 17),
    ("TestLoadedHost", 13),
    ("TestTwoHosts", 12),
    ("TestUserOnSingleHost", 10),
    ("TestHostStartsWhenExedDown", 10),
]


def detect_changes():
    """Return (exe_changed, shelley_changed, blog_changed, ui_changed) by diffing against origin/main."""
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
        return True, False, False, True

    exe_changed = False
    shelley_changed = False
    blog_changed = False
    ui_changed = False
    for f in files:
        if f.startswith("shelley/") or f == ".github/workflows/shelley-tests.yml":
            shelley_changed = True
        elif f.startswith("blog/") or f.startswith("cmd/blogd/"):
            blog_changed = True
        elif f.startswith("observability/"):
            pass  # observability-only changes don't need exe tests
        else:
            exe_changed = True
        # ui/ changes are also exe changes (exed embeds ui/dist), but
        # tracked separately so we can run ui-specific typecheck + vitest.
        if f.startswith("ui/"):
            ui_changed = True

    return exe_changed, shelley_changed, blog_changed, ui_changed


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


def split_exelets_heavy_tests(n_shards):
    """LPT-greedy partition of EXELETS_HEAVY_TEST_COSTS across n_shards.
    Returns list of lists of test names; the heaviest tests land on
    shard 1 first, so shard 1 is the busiest."""
    tests = sorted(EXELETS_HEAVY_TEST_COSTS, key=lambda t: -t[1])
    shards = [[] for _ in range(n_shards)]
    loads = [0] * n_shards
    for name, cost in tests:
        i = loads.index(min(loads))
        shards[i].append(name)
        loads[i] += cost
    return shards


def split_migration_tests(n_shards):
    """Split MIGRATION_TEST_COSTS into n_shards balanced groups.

    Uses longest-processing-time-first greedy partitioning: assign each
    test (in decreasing cost order) to the currently-lightest shard.
    Produces an (up to 4/3)-approximation of the optimal makespan, which
    is plenty for 5 items and 2 shards.

    Returns a list of lists of test names.
    """
    tests = sorted(MIGRATION_TEST_COSTS, key=lambda t: -t[1])
    shards = [[] for _ in range(n_shards)]
    loads = [0] * n_shards
    for name, cost in tests:
        i = loads.index(min(loads))
        shards[i].append(name)
        loads[i] += cost
    return shards


def _generate_migration_shards_text(exelets_vm_concurrency, migration_shards, coverage):
    """YAML for the exelets-migration shard steps. Sharded because the
    TestDirectMigration* tests are serial and dominate the critical path."""
    lines = []
    shards = split_migration_tests(migration_shards)
    for i, tests in enumerate(shards):
        shard_num = i + 1
        label = f"migration-{shard_num}"
        # -run filter: anchored exact-match alternation so "TestDirectMigration"
        # doesn't accidentally match "TestDirectMigrationResumable".
        run_filter = "^(" + "|".join(tests) + ")$"
        lines.append(f'- label: ":arrow_right_hook: e1e migration ({shard_num}/{migration_shards})"')
        lines.append(f'  key: test-exelets-{label}')
        lines.append('  depends_on:')
        lines.append('    - build-e1e')
        lines.append('    - ensure-snapshot')
        lines.append('  command: python3 .buildkite/steps/test-e1e-exelets.py')
        lines.append('  timeout_in_minutes: 10')
        lines.append('  env:')
        lines.append('    VM_DRIVER: cloudhypervisor')
        lines.append(f'    E1E_EXELETS_VM_CONCURRENCY: "{exelets_vm_concurrency}"')
        lines.append(f'    E1E_EXELETS_RUN_FILTER: "{run_filter}"')
        lines.append(f'    E1E_EXELETS_LABEL: "{label}"')
        # Direct-migration tests are bottlenecked on zfs send/recv CPU inside
        # the outer CI VM. Give these shards more vCPUs (default is 4).
        lines.append('    VCPUS: "8"')
        if coverage:
            lines.append(f'    E1E_COVERAGE: "true"')
        lines.append('  artifact_paths:')
        lines.append(f'    - "e1e-results-{label}.json"')
        lines.append(f'    - "test-gantt-{label}.html"')
        lines.append(f'    - "e1e-results-{label}.xml"')
        lines.append(f'    - "e1e-logs-{label}/**/*"')
        if coverage:
            lines.append(f'    - "coverage-{label}.txt"')
        lines.append('')
    return "\n".join(lines)


def generate_migration_only_steps(exelets_vm_concurrency, migration_shards, coverage=False):
    """YAML for just the exelets-migration shards, used by the Migration-Only
    trailer for fast iteration on migration tests."""
    return _generate_migration_shards_text(exelets_vm_concurrency, migration_shards, coverage)


# When a single-letter shard (e.g. ("T", "T")) is too heavy for letter-
# rebalancing to fix, split it further by structural prefix-classes over
# Test<L>.* — i.e. the regexes partition all conceivable test names
# starting with the letter, so coverage is provable from the regex shape
# alone (no enumeration of test names, no drift hazard if a TestT* is
# added or renamed).
#
# Each entry maps a single letter to an ordered list of (suffix, regex)
# pairs. The regexes are anchored at start; they MUST partition every
# string beginning with Test<letter>. Verify by inspection: the cases
# below exhaust the possibilities by walking down the prefix character
# by character.
#
# Tuning: rebalance suffix-classes when one sub-shard's wall consistently
# dominates. Last tuned 2026-04-29 from #1965 timing data.
LETTER_SUBSPLIT = {
    # T tests are dominated by TestTeam*. Partition into:
    #   T-1: anything Test<T> that is NOT TestTeam[S-Z]*
    #   T-2: TestTeam[S-Z]*  (TeamSharing, TeamSSHSharing, TeamTransfer,
    #                         TeamUnenrollForce, ...)
    # Coverage proof: every string starting with TestT either (a) ends
    # there, (b) has a non-e at position 5, (c) has a non-a at position
    # 6, (d) has a non-m at position 7, or (e) has TestTeam followed by
    # some letter — [A-R] (T-1) or [S-Z] (T-2). The ($|...) alternations
    # cover the "ends here" cases so a hypothetical bare 'TestTeam' test
    # wouldn't be silently dropped.
    "T": [
        ("1", r"^Test(T($|[^Ee])|Te($|[^Aa])|Tea($|[^Mm])|Team($|[A-Ra-r]))"),
        ("2", r"^TestTeam[S-Zs-z]"),
    ],
}


def _generate_exelets_shards_text(exelets_vm_concurrency, exelets_shards, coverage):
    """YAML for the e1e exelets step, optionally split into N shards by
    LPT-greedy partition of the heavy non-parallel tests.

    Shard 1 runs everything not assigned to another shard (testinfra +
    parallel TestEmail/TestMetadata + its own subset of heavy tests).
    Shards 2..N run only their assigned heavy tests via -run filter.

    All shards skip TestDirectMigration* (those run in dedicated
    migration shards)."""
    if exelets_shards <= 1:
        # Single-shard form: keep the existing label/key for backwards
        # compatibility with dashboards and the test-keys collector.
        return _generate_exelets_single_shard_text(exelets_vm_concurrency, coverage)

    heavy_assigned = split_exelets_heavy_tests(exelets_shards)
    # Build skip-filter for shard 1: skip TestDirectMigration* and any
    # heavy test assigned to shards 2..N.
    others = [t for shard in heavy_assigned[1:] for t in shard]
    shard1_skip = "^(" + "|".join(["TestDirectMigration"] + [f"{t}$" for t in others]) + ")"

    lines = []
    for i in range(exelets_shards):
        shard_num = i + 1
        label = f"exelets-{shard_num}" if exelets_shards > 1 else "exelets"
        lines.append(f'- label: ":electric_plug: e1e exelets ({shard_num}/{exelets_shards})"')
        lines.append(f'  key: test-exelets-{shard_num}' if exelets_shards > 1 else '  key: test-exelets')
        lines.append('  depends_on:')
        lines.append('    - build-e1e')
        lines.append('    - ensure-snapshot')
        lines.append('  command: python3 .buildkite/steps/test-e1e-exelets.py')
        lines.append('  timeout_in_minutes: 10')
        lines.append('  env:')
        lines.append('    VM_DRIVER: cloudhypervisor')
        lines.append(f'    E1E_EXELETS_VM_CONCURRENCY: "{exelets_vm_concurrency}"')
        lines.append(f'    E1E_EXELETS_LABEL: "{label}"')
        if i == 0:
            # Shard 1 runs everything not assigned to other shards.
            lines.append(f'    E1E_EXELETS_SKIP_FILTER: "{shard1_skip}"')
        else:
            run_filter = "^(" + "|".join(heavy_assigned[i]) + ")$"
            lines.append(f'    E1E_EXELETS_RUN_FILTER: "{run_filter}"')
            # Still skip migration tests defensively.
            lines.append('    E1E_EXELETS_SKIP_FILTER: "TestDirectMigration"')
        if coverage:
            lines.append('    E1E_COVERAGE: "true"')
        lines.append('  artifact_paths:')
        lines.append(f'    - "e1e-results-{label}.json"')
        lines.append(f'    - "test-gantt-{label}.html"')
        lines.append(f'    - "e1e-results-{label}.xml"')
        lines.append(f'    - "e1e-logs-{label}/**/*"')
        if coverage:
            lines.append(f'    - "coverage-{label}.txt"')
        lines.append('')
    return "\n".join(lines)


def _generate_exelets_single_shard_text(exelets_vm_concurrency, coverage):
    lines = []
    lines.append('- label: ":electric_plug: e1e exelets"')
    lines.append('  key: test-exelets')
    lines.append('  depends_on:')
    lines.append('    - build-e1e')
    lines.append('    - ensure-snapshot')
    lines.append('  command: python3 .buildkite/steps/test-e1e-exelets.py')
    lines.append('  timeout_in_minutes: 10')
    lines.append('  env:')
    lines.append('    VM_DRIVER: cloudhypervisor')
    lines.append(f'    E1E_EXELETS_VM_CONCURRENCY: "{exelets_vm_concurrency}"')
    lines.append('    E1E_EXELETS_SKIP_FILTER: "TestDirectMigration"')
    if coverage:
        lines.append('    E1E_COVERAGE: "true"')
    lines.append('  artifact_paths:')
    lines.append('    - "e1e-results-exelets.json"')
    lines.append('    - "test-gantt-exelets.html"')
    lines.append('    - "e1e-results-exelets.xml"')
    lines.append('    - "e1e-logs-exelets/**/*"')
    if coverage:
        lines.append('    - "coverage-exelets.txt"')
    lines.append('')
    return "\n".join(lines)


def generate_e1e_steps(n_shards, vm_concurrency, gomaxprocs, exelets_vm_concurrency, migration_shards, exelets_shards, coverage=False):
    """Generate YAML text for e1e shard steps + exelets step."""
    shards_in = split_letters(n_shards)
    # Expand any single-letter shard that has a structural sub-split rule.
    shards = []
    for s, e in shards_in:
        if s == e and s in LETTER_SUBSPLIT:
            for suffix, regex in LETTER_SUBSPLIT[s]:
                shards.append((f"{s}-{suffix}", regex))
        else:
            shards.append((f"{s}-{e}", f"^Test[{s}-{e}{s.lower()}-{e.lower()}]"))
    lines = []

    for i, (shard_label, run_filter) in enumerate(shards):
        shard_num = i + 1
        label = f":rocket: e1e tests ({shard_label})"

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
        if coverage:
            lines.append(f'    E1E_COVERAGE: "true"')
        lines.append(f'  artifact_paths:')
        lines.append(f'    - "recordings-{shard_num}.html"')
        lines.append(f'    - "test-gantt-{shard_num}.html"')
        lines.append(f'    - "e1e-results-{shard_num}.json"')
        lines.append(f'    - "e1e-results-{shard_num}.xml"')
        lines.append(f'    - "e1e-logs-{shard_num}/**/*"')
        if coverage:
            lines.append(f'    - "coverage-e1e-{shard_num}.txt"')
        lines.append('')

    # Exelets step(s) (all tests except direct migration). Sharded
    # because TestExedStartsWithDownHost + TestLoadedHost + TestTwoHosts +
    # TestUserOnSingleHost + TestHostStartsWhenExedDown share global
    # serverEnv state and cannot use t.Parallel(); sharding lets pairs
    # run on separate VM hosts in parallel.
    lines.append(_generate_exelets_shards_text(exelets_vm_concurrency, exelets_shards, coverage))

    # Exelets migration steps (direct migration tests, parallel with above).
    lines.append(_generate_migration_shards_text(exelets_vm_concurrency, migration_shards, coverage))

    # Billing e1e step (separate exed instance with billing enabled).
    lines.append('- label: ":credit_card: e1e billing"')
    lines.append('  key: test-billing')
    lines.append('  depends_on:')
    lines.append('    - build-e1e')
    lines.append('    - ensure-snapshot')
    lines.append('  command: python3 .buildkite/steps/test-e1e-billing.py')
    lines.append('  timeout_in_minutes: 15')
    lines.append('  env:')
    lines.append('    VM_DRIVER: cloudhypervisor')
    lines.append('  artifact_paths:')
    lines.append('    - "e1e-results-billing.json"')
    lines.append('    - "test-gantt-billing.html"')
    lines.append('    - "e1e-logs-billing/**/*"')
    lines.append('')

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


def _inject_coverage_env(segment_text):
    """Add E1E_COVERAGE: "true" to relevant steps in the segment.

    Injects into steps that have an env: block, and adds an env: block
    to the build-e1e step (which builds the binaries with/without coverage).
    """
    lines = segment_text.splitlines()
    result = []
    in_step_key = None
    i = 0
    while i < len(lines):
        line = lines[i]
        stripped = line.strip()

        # Track current step key.
        if stripped.startswith('key:'):
            in_step_key = stripped.split(':', 1)[1].strip()

        result.append(line)

        # Inject after existing env: blocks.
        if stripped == 'env:':
            indent = len(line) - len(line.lstrip())
            result.append(f"{' ' * indent}  E1E_COVERAGE: \"true\"")

        # For build-e1e (no env: block), add one after command: line.
        if in_step_key == 'build-e1e' and stripped.startswith('command:'):
            indent = len(line) - len(line.lstrip())
            result.append(f"{' ' * indent}env:")
            result.append(f"{' ' * indent}  E1E_COVERAGE: \"true\"")

        i += 1
    return '\n'.join(result)


def generate_coverage_merge_step(all_test_keys):
    """Generate a step that merges all coverage profiles.

    all_test_keys: list of step keys to depend on (unit + e1e + exelets).
    """
    lines = [
        '- label: ":bar_chart: merge coverage"',
        '  key: merge-coverage',
        '  allow_dependency_failure: true',
        '  depends_on:',
    ]
    for key in all_test_keys:
        lines.append(f'    - {key}')
    lines += [
        '  command: python3 .buildkite/steps/merge-coverage.py',
        '  timeout_in_minutes: 10',
        '  artifact_paths:',
        '    - "coverage-merged.txt"',
        '    - "coverage-summary.txt"',
        '    - "coverage-report.html"',
    ]
    return '\n'.join(lines)


def generate_psimon_step(all_keys):
    """Generate a final step that collects psimon pressure data."""
    lines = [
        '- label: ":chart_with_upwards_trend: collect CI pressure"',
        '  key: collect-psimon',
        '  allow_dependency_failure: true',
        '  depends_on:',
    ]
    for key in all_keys:
        lines.append(f'    - {key}')
    lines += [
        '  command: .buildkite/steps/collect-psimon.sh',
        '  timeout_in_minutes: 2',
        '  artifact_paths:',
        '    - "psimon-pressure.html"',
    ]
    return '\n'.join(lines)


def main():
    # Record build start time for psimon to use later.
    try:
        subprocess.run(
            ["buildkite-agent", "meta-data", "set", "psimon-start", str(int(time.time()))],
            check=False, capture_output=True,
        )
    except FileNotFoundError:
        pass  # Not running in Buildkite

    exe_changed, shelley_changed, blog_changed, ui_changed = detect_changes()

    branch = os.environ.get("BUILDKITE_BRANCH", "")
    is_queue = branch.startswith("kite-queue-")

    # Read tuning knobs from commit trailers, then env vars, then defaults.
    trailers = read_commit_trailers()
    n_shards = int(trailers.get("e1e-shards", os.environ.get("E1E_SHARDS", "5")))
    vm_concurrency = trailers.get("e1e-vm-concurrency", os.environ.get("E1E_VM_CONCURRENCY", "12"))
    gomaxprocs = trailers.get("e1e-gomaxprocs", os.environ.get("E1E_GOMAXPROCS", ""))
    exelets_vm_concurrency = trailers.get("e1e-exelets-vm-concurrency", os.environ.get("E1E_EXELETS_VM_CONCURRENCY", "10"))
    # 3 is the sweet spot: 4 parallel migration shards run into CPU
    # contention with the 5 e1e shards + exelets step on the 48-core CI
    # host, inflating per-test times (e.g. build #1186 saw Orphaned go
    # 30s -> 50s). Stick with 3 until contention is reduced.
    # 4 is the sweet spot on exe-ci-03 (AMD EPYC 9455, 48c): benchmarks
    # (#1940 vs #1954) show migration step max wall drops from ~76s
    # (3 shards) to ~60s (4) without measurable contention with the 5
    # e1e shards. The older exe-ci-01 host saw contention at 4 shards;
    # if we ever route production traffic back through it, reset to 3
    # via the E1E-Migration-Shards trailer.
    #
    # 2026-04-29: bumped 4->5 once exelets sharding freed up agent
    # slots; #1976 shows max migration wall 70->54s with 5 shards.
    migration_shards = int(trailers.get("e1e-migration-shards", os.environ.get("E1E_MIGRATION_SHARDS", "5")))
    # Number of shards for the e1e exelets step. The 5 heavy non-parallel
    # tests (TestExedStartsWithDownHost, TestLoadedHost, TestTwoHosts,
    # TestUserOnSingleHost, TestHostStartsWhenExedDown) total ~62s of
    # serial work, dominating the build wall as the critical path. With
    # 2 shards LPT-greedy assigns 17+10+10=37 to shard 1 and 13+12=25 to
    # shard 2; pair with shard 1's ~6s VM/exelet startup + ~9s teardown
    # the shards converge to ~50-55s, dropping the critical path ~25%.
    exelets_shards = int(trailers.get("e1e-exelets-shards", os.environ.get("E1E_EXELETS_SHARDS", "2")))
    coverage = trailers.get("coverage", os.environ.get("E1E_COVERAGE", "")).lower() in ("true", "1", "yes")
    # Test-Race: true (default) / false. Also accept EXE_TEST_RACE env.
    test_race = trailers.get("test-race", os.environ.get("EXE_TEST_RACE", "true")).lower() not in ("false", "0", "no")
    # Bypass-CI: author asserts the change needs no tests (docs, etc.).
    # Honored only on queue/test branches; main always runs the full suite.
    # Require exactly "true" (case-insensitive) — no "1"/"yes" shorthand, to
    # keep the trailer greppable and intentional.
    bypass_ci = (
        trailers.get("bypass-ci", "").strip().lower() == "true"
        and branch != "main"
    )
    # Migration-Only: true — skip everything except exelets-migration shards.
    # For fast iteration on migration tests only. Honored only off main.
    migration_only = (
        trailers.get("migration-only", "").strip().lower() == "true"
        and branch != "main"
    )

    print(f"exe_changed={exe_changed} shelley_changed={shelley_changed} "
          f"blog_changed={blog_changed} ui_changed={ui_changed} "
          f"is_queue={is_queue} branch={branch} "
          f"e1e_shards={n_shards} vm_concurrency={vm_concurrency} "
          f"gomaxprocs={gomaxprocs or '(default)'} "
          f"exelets_vm_concurrency={exelets_vm_concurrency} "
          f"migration_shards={migration_shards} "
          f"exelets_shards={exelets_shards} "
          f"coverage={coverage} "
          f"bypass_ci={bypass_ci}",
          file=sys.stderr)

    # Assemble step segments
    segments = []

    # Always: commit validation
    segments.append(load_segment("commit-validation.yml"))

    # Conditional: exe tests
    if exe_changed and not bypass_ci:
        if migration_only:
            # Only include exelets-migration shards (and their prerequisites).
            segments.append(load_segment("exe.yml"))
            segments.append(generate_migration_only_steps(exelets_vm_concurrency, migration_shards, coverage=coverage))
        else:
            exe_segment = load_segment("exe.yml")
            if coverage:
                exe_segment = _inject_coverage_env(exe_segment)
            segments.append(exe_segment)
            # Generate e1e steps dynamically
            segments.append(generate_e1e_steps(n_shards, vm_concurrency, gomaxprocs, exelets_vm_concurrency, migration_shards, exelets_shards, coverage=coverage))

    # Conditional: shelley tests
    if shelley_changed and not bypass_ci:
        segments.append(load_segment("shelley.yml"))

    # Conditional: blog tests (run for blog-only changes, or as part of exe)
    if (blog_changed or exe_changed) and not bypass_ci:
        segments.append(load_segment("blog.yml"))

    # Conditional: ui tests (typecheck + vitest). Runs whenever ui/ changes.
    if ui_changed and not bypass_ci:
        segments.append(load_segment("ui.yml"))

    # Always: formatting
    segments.append(load_segment("format.yml"))

    # Coverage: add merge step that depends on all test steps.
    if coverage and exe_changed:
        # Collect keys of all test steps that produce coverage.
        test_keys = collect_step_keys("\n".join(segments))
        # Filter to only test step keys (unit-*, test-e1e-*, test-exelets).
        coverage_deps = [k for k in test_keys if k.startswith("unit-") or k.startswith("test-e1e-") or k == "test-exelets" or k.startswith("test-exelets-")]
        segments.append(generate_coverage_merge_step(coverage_deps))

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

        # Failure notification step — runs even when tests fail.
        # Success notifications are handled by rebase-and-push.py.
        notify_lines = [
            '- label: ":slack: notify"',
            '  key: notify-slack',
            '  allow_dependency_failure: true',
            '  depends_on:',
        ]
        for k in all_keys:
            notify_lines.append(f'    - {k}')
        notify_lines += [
            '  command: python3 .buildkite/steps/notify-slack.py',
            '  timeout_in_minutes: 5',
        ]
        segments.append('\n'.join(notify_lines))

    # psimon: collect CI machine pressure data as the very last step.
    final_keys = collect_step_keys("\n".join(segments))
    psimon_text = generate_psimon_step(final_keys)
    segments.append(psimon_text)

    # Emit the pipeline (stdout for upload, stderr for build log).
    #
    # Pin all jobs in this build to the SAME host that ran the generate step.
    # Multiple CI machines (exe-ci-01, exe-ci-03, ...) share the exe-ci queue,
    # but each build's caches/git-mirrors/snapshots are local to one host —
    # splitting jobs across hosts wastes those caches and risks dataset name
    # collisions. The hostname tag is set by tags-from-host on each agent
    # (see .buildkite/agent/buildkite-agent.cfg).
    #
    # Queue can be overridden by the CI-Queue: <name> commit trailer or the
    # EXE_CI_QUEUE env var (default exe-ci). Use CI-Queue: exe-ci-test to
    # target the staging queue (e.g. for benchmarking new hardware).
    default_queue = os.environ.get("EXE_CI_QUEUE", "exe-ci")
    queue = trailers.get("ci-queue", default_queue).strip()
    # Pin to the host running the generate step ONLY when targeting the
    # default queue (i.e. no queue override). When a CI-Queue trailer
    # redirects to e.g. exe-ci-test, the generate step ran on exe-ci but
    # the jobs need to land on a different host — don't pin in that case.
    pin_host = ""
    if queue == default_queue:
        # CI-Host: <hostname> trailer forces the entire build onto a
        # specific agent host. Useful for reproducible benchmarking —
        # without this the host that picked up :pipeline: silently
        # defines the run, making A/B comparisons noisy.
        pin_host = trailers.get("ci-host", "").strip()
        if not pin_host:
            pin_host = os.environ.get("BUILDKITE_AGENT_META_DATA_HOSTNAME", "").strip()
        if not pin_host:
            # Fallback for local testing: ask the OS.
            try:
                pin_host = subprocess.check_output(["hostname"], text=True).strip()
            except Exception:
                pin_host = ""
    lines = ["agents:", f"  queue: {queue}"]
    if pin_host:
        lines.append(f"  hostname: {pin_host}")
    lines.append("")
    if not test_race:
        lines += ["env:", '  EXE_TEST_RACE: "false"', ""]
    lines.append("steps:")
    for seg in segments:
        for line in seg.rstrip().splitlines():
            lines.append(f"  {line}")
        lines.append("")

    output = "\n".join(lines)
    print(output)
    print(output, file=sys.stderr)


if __name__ == "__main__":
    main()
