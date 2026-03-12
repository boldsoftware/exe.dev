#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6"]
# ///
"""RLM-based documentation review for exe.dev.

Uses a dSPy RLM (Recursive Language Model) to iteratively explore docs
and cross-reference against source code.

Requires: ANTHROPIC_API_KEY, deno

Usage:
    uv run scripts/e4e_rlm.py                  # run review
    uv run scripts/e4e_rlm.py --verbose         # with RLM REPL traces
"""

import argparse
import os
import subprocess
import sys
import time

import dspy

PROMPT = """\
Please do a thorough read-through of the user-facing exe.dev docs, located in directory "docs/content".

The rest of the repo is included, so that you can compare the docs against the actual behavior of the system.

Please flag any issues with the user-facing docs, including but not limited to:

- Technical inaccuracies
- Vagueness, contradictions, missing context, or steps that would confuse or mislead a user
- Mismatched terminology
- Conflicting instructions
- Outdated or missing cross-links

Do not flag missing discussions of limits or caps; these are intentionally omitted.

We take pride in our docs. Help us keep them worthy of that.

Your final report should be concise and efficient. It will be read by busy engineers who already know the codebase well.

Use Slack's mrkdwn syntax.

If there are no findings, the report should consist only of the word "OK", on its own line."""


class DocReview(dspy.Signature):
    __doc__ = PROMPT

    docs: dict[str, str] = dspy.InputField(
        desc="Documentation files from docs/. Keys are relative paths, values are contents."
    )

    source: dict[str, str] = dspy.InputField(
        desc="Source code files from the repo. Keys are relative paths, values are contents. "
        "Use this to verify documentation claims against the actual implementation."
    )

    report: str = dspy.OutputField(
        desc="The documentation review report. Slack mrkdwn. 'OK' if no findings."
    )


def load_docs(repo_root: str) -> dict[str, str]:
    docs = {}
    docs_dir = os.path.join(repo_root, "docs")
    for root, _, files in os.walk(docs_dir):
        for f in files:
            if not f.endswith((".md", ".html")):
                continue
            full_path = os.path.join(root, f)
            rel = os.path.relpath(full_path, repo_root)
            try:
                with open(full_path) as fh:
                    docs[rel] = fh.read()
            except Exception:
                pass
    return docs


def load_source(repo_root: str) -> dict[str, str]:
    """Load all non-test Go source files."""
    source = {}
    for root, dirs, files in os.walk(repo_root):
        # Skip irrelevant directories.
        base = os.path.basename(root)
        if base in (".git", "vendor", "node_modules", "deps", "__pycache__"):
            dirs.clear()
            continue
        for f in files:
            if not f.endswith(".go"):
                continue
            if f.endswith("_test.go"):
                continue
            full_path = os.path.join(root, f)
            rel = os.path.relpath(full_path, repo_root)
            try:
                with open(full_path) as fh:
                    source[rel] = fh.read()
            except Exception:
                pass
    return source


def log(msg):
    print(f"[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def main():
    parser = argparse.ArgumentParser(
        description="RLM-based documentation review for exe.dev"
    )
    parser.add_argument(
        "--verbose", action="store_true",
        help="Print RLM REPL traces to stdout",
    )
    args = parser.parse_args()

    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("error: ANTHROPIC_API_KEY must be set", file=sys.stderr)
        sys.exit(1)

    r = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True
    )
    if r.returncode != 0:
        print("error: not inside a git repository", file=sys.stderr)
        sys.exit(1)
    repo_root = r.stdout.strip()

    os.environ["DENO_NO_PACKAGE_JSON"] = "1"

    log("loading docs")
    docs = load_docs(repo_root)
    log(f"loaded {len(docs)} doc files")

    log("loading source")
    source = load_source(repo_root)
    log(f"loaded {len(source)} source files ({sum(len(v) for v in source.values()) // 1024}KB)")

    lm = dspy.LM("anthropic/claude-opus-4-6")
    dspy.configure(lm=lm)

    rlm = dspy.RLM(
        DocReview,
        max_iterations=15,
        max_llm_calls=30,
        verbose=args.verbose,
    )

    log("running RLM doc review")
    result = rlm(docs=docs, source=source)
    report = result.report.strip()

    print()
    print(report)
    print()

    sys.exit(0 if report == "OK" or report.endswith("\nOK") else 1)


if __name__ == "__main__":
    main()
