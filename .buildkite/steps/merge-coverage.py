#!/usr/bin/env python3
"""Merge coverage profiles from all test steps into a single report.

Downloads coverage-*.txt artifacts from earlier steps, merges them
using Go's cover tool, and produces:
  - coverage-merged.txt  (combined Go coverage profile)
  - coverage-summary.txt (per-package coverage percentages)
  - coverage-report.html (HTML report for browsing)
"""

import glob
import os
import subprocess
import sys


def run(args, **kwargs):
    print(f"+ {' '.join(args)}", flush=True)
    return subprocess.run(args, check=True, **kwargs)


def main():
    os.environ["PATH"] = "/usr/local/go/bin:" + os.environ.get("HOME", "") + "/go/bin:" + os.environ["PATH"]

    print("--- :bar_chart: Download coverage artifacts", flush=True)
    os.makedirs("coverage-parts", exist_ok=True)

    # Download all coverage-*.txt artifacts from this build.
    dl = subprocess.run(
        ["buildkite-agent", "artifact", "download", "coverage-*.txt", "coverage-parts/"],
    )
    if dl.returncode != 0:
        print("WARNING: Failed to download coverage artifacts", flush=True)
        sys.exit(0)  # non-fatal

    profiles = sorted(glob.glob("coverage-parts/coverage-*.txt"))
    if not profiles:
        print("No coverage profiles found", flush=True)
        sys.exit(0)

    print(f"Found {len(profiles)} coverage profiles:", flush=True)
    for p in profiles:
        size = os.path.getsize(p)
        print(f"  {p} ({size} bytes)", flush=True)

    print("--- :merge: Merge coverage profiles", flush=True)

    # Go coverage profiles have a header line "mode: atomic" (or set/count)
    # followed by coverage data lines. To merge, we keep the header from the
    # first file and concatenate data lines from all files.
    merged = "coverage-merged.txt"
    header_written = False
    with open(merged, "w") as out:
        for p in profiles:
            with open(p) as f:
                for line in f:
                    if line.startswith("mode:"):
                        if not header_written:
                            out.write(line)
                            header_written = True
                        continue
                    if line.strip():
                        out.write(line)

    if not header_written:
        print("WARNING: No valid coverage data found in profiles", flush=True)
        sys.exit(0)

    merged_size = os.path.getsize(merged)
    print(f"Merged profile: {merged} ({merged_size} bytes)", flush=True)

    # Generate per-package summary.
    print("--- :clipboard: Generate coverage summary", flush=True)
    summary_result = subprocess.run(
        ["go", "tool", "cover", "-func", merged],
        capture_output=True, text=True,
    )
    if summary_result.returncode == 0:
        summary = summary_result.stdout
        with open("coverage-summary.txt", "w") as f:
            f.write(summary)

        # Print total line and top uncovered.
        lines = summary.strip().splitlines()
        total_line = [l for l in lines if "total:" in l.lower()]
        if total_line:
            print(f"\n{total_line[0]}", flush=True)

        # Annotate in Buildkite.
        _annotate_coverage(lines)
    else:
        print(f"WARNING: go tool cover -func failed: {summary_result.stderr}", flush=True)

    # Generate HTML report.
    print("--- :globe_with_meridians: Generate HTML coverage report", flush=True)
    html_result = subprocess.run(
        ["go", "tool", "cover", "-html", merged, "-o", "coverage-report.html"],
    )
    if html_result.returncode != 0:
        print("WARNING: HTML coverage report generation failed", flush=True)

    print("--- :white_check_mark: Coverage merge complete", flush=True)


def _annotate_coverage(summary_lines):
    """Post a Buildkite annotation with coverage summary."""
    if os.environ.get("BUILDKITE") != "true":
        return

    # Parse summary into package -> coverage%
    packages = []
    total = None
    for line in summary_lines:
        parts = line.split()
        if len(parts) >= 3 and parts[-1].endswith("%"):
            pkg = parts[0]
            pct = parts[-1]
            if "total:" in line.lower():
                total = pct
            else:
                packages.append((pkg, pct))

    md_lines = ["**Code Coverage Report**\n"]
    if total:
        md_lines.append(f"Overall coverage: **{total}**\n")

    # Show packages with lowest coverage (most interesting).
    # Sort by coverage percentage ascending.
    def pct_val(s):
        try:
            return float(s.rstrip("%"))
        except ValueError:
            return 100.0

    packages.sort(key=lambda x: pct_val(x[1]))

    if packages:
        md_lines.append("\n**Lowest coverage packages (bottom 20):**\n")
        md_lines.append("| Package | Coverage |")
        md_lines.append("|---------|----------|")
        for pkg, pct in packages[:20]:
            md_lines.append(f"| `{pkg}` | {pct} |")

    annotation = "\n".join(md_lines)
    subprocess.run(
        ["buildkite-agent", "annotate", "--context", "coverage", "--style", "info"],
        input=annotation, text=True,
    )


if __name__ == "__main__":
    main()
