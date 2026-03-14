#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6", "python-dotenv"]
# ///
"""Ad-hoc query agent with ClickHouse and local worktree sources.

Connects data sources through the proxy/sandbox/RLM pipeline, then
answers a specific question.

Usage:
    uv run python3 panopticon/logs.py "how many errors in the last hour?"
    uv run python3 panopticon/logs.py --sources worktree "what does the proxy system do?"
    uv run python3 panopticon/logs.py --sources clickhouse,worktree --verbose "correlate recent deploys with error spikes"

Reads panopticon/.env automatically (python-dotenv).
"""

import argparse
import logging
import os
from contextlib import nullcontext
from pathlib import Path
import sys
from datetime import datetime, timezone

# Project root on path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import dspy

from panopticon.logs_prompts import (
    CLICKHOUSE_DESC,
    CODE_DESC,
    COMMITS_DESC,
    QUERY_INSTRUCTION,
    SOURCE_DESCS,
)
from deps.dspy.predict.rlm import RLM

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("logs")

VALID_SOURCES = {"clickhouse", "worktree"}


def _require_env(name: str) -> str:
    """Read an env var, fail fast with a clear message if missing."""
    val = os.environ.get(name, "").strip()
    if not val:
        print(f"Error: {name} must be set. See panopticon/.env.example.", file=sys.stderr)
        sys.exit(1)
    return val


def query(question: str, *, sources: set[str] | None = None, verbose: bool = False) -> str:
    """Run the RLM pipeline and return the answer."""
    if sources is None:
        sources = VALID_SOURCES

    _require_env("ANTHROPIC_API_KEY")

    sig_fields: dict = {}
    call_kwargs: dict = {}
    tools: list = []
    needs_proxy = False

    # --- ClickHouse (proxy-based) ---
    if "clickhouse" in sources:
        from panopticon.mux import MuxServer
        from panopticon.proxy import ProxyRegistry
        from panopticon.sources.clickhouse import ClickHouseClient, ClickHouseSource

        needs_proxy = True
        registry = ProxyRegistry()

        ch_url = _require_env("EXE_CLICKHOUSE_URL")
        ch_password = _require_env("EXE_CLICKHOUSE_PASSWORD")
        ch_user = os.environ.get("EXE_CLICKHOUSE_USER", "readonly").strip() or "readonly"
        ch_database = os.environ.get("EXE_CLICKHOUSE_DATABASE", "").strip() or None

        ch_client = ClickHouseClient(ch_url, user=ch_user, password=ch_password)
        clickhouse = ClickHouseSource(ch_client, database=ch_database)
        registry.register(clickhouse)
        sig_fields["clickhouse"] = dspy.InputField(desc=CLICKHOUSE_DESC)
        call_kwargs["clickhouse"] = clickhouse

    # --- Worktree (direct loading, classic RLM) ---
    if "worktree" in sources:
        from panopticon.sources.worktree import Worktree

        wt = Worktree(os.path.dirname(os.path.abspath(__file__)))
        log.info("Loading worktree from %s...", wt.root)
        sig_fields["code"] = dspy.InputField(desc=CODE_DESC)
        sig_fields["commits"] = dspy.InputField(desc=COMMITS_DESC)
        call_kwargs["code"] = wt.code
        log.info("Loaded %d files.", len(wt.code))
        call_kwargs["commits"] = wt.commits
        log.info("Loaded %d commits.", len(wt.commits))
        tools.append(wt.commit_diff)

    # --- Dynamic sources description ---
    source_names = sorted(sources)
    descs = [SOURCE_DESCS[s] for s in source_names]
    if len(descs) == 1:
        sources_desc = descs[0]
    elif len(descs) == 2:
        sources_desc = f"{descs[0]} and {descs[1]}"
    else:
        sources_desc = ", ".join(descs[:-1]) + f", and {descs[-1]}"

    log.info("Starting agent (sources: %s)...", ", ".join(source_names))

    if needs_proxy:
        from panopticon.mux import MuxServer
        mux_ctx = MuxServer(registry)
    else:
        mux_ctx = nullcontext()

    with mux_ctx as mux:
        if mux:
            log.info("MuxServer on %s", mux.socket_path)

        lm = dspy.LM("anthropic/claude-opus-4-6", max_tokens=16384)
        dspy.configure(lm=lm)

        now = datetime.now(timezone.utc)
        date_str = now.strftime("%Y-%m-%d")
        time_str = now.strftime("%H:%M UTC")

        instruction = QUERY_INSTRUCTION.format(
            date_str=date_str,
            time_str=time_str,
            question=question,
            sources_desc=sources_desc,
        )

        sig_fields["answer"] = dspy.OutputField()
        sig = dspy.Signature(sig_fields, instruction)

        rlm_kwargs = dict(
            max_iterations=20,
            max_llm_calls=50,
            verbose=verbose,
        )
        if mux:
            rlm_kwargs["uds_path"] = mux.socket_path
        if tools:
            rlm_kwargs["tools"] = tools

        rlm = RLM(sig, **rlm_kwargs)

        log.info("Running RLM agent...")
        result = rlm(**call_kwargs)
        return result.answer


def main():
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parent / ".env")

    parser = argparse.ArgumentParser(description="Ad-hoc query agent")
    parser.add_argument(
        "question", nargs="?", default=None,
        help="The question to answer",
    )
    parser.add_argument(
        "--query", "-q", default=None,
        help="The question to answer (alternative to positional arg)",
    )
    parser.add_argument(
        "--sources",
        default="clickhouse,worktree",
        help="Comma-separated sources to enable (default: clickhouse,worktree)",
    )
    parser.add_argument(
        "--verbose", action="store_true",
        help="Print RLM REPL traces to stdout",
    )
    args = parser.parse_args()

    question = args.question or args.query
    if not question:
        parser.error("provide a question as a positional arg or via --query")

    enabled = {s.strip().lower() for s in args.sources.split(",")}
    enabled.discard("")
    unknown = enabled - VALID_SOURCES
    if unknown:
        print(f"Error: unknown source(s): {', '.join(sorted(unknown))}. "
              f"Valid: {', '.join(sorted(VALID_SOURCES))}", file=sys.stderr)
        sys.exit(1)
    if not enabled:
        print("Error: at least one source must be enabled via --sources.", file=sys.stderr)
        sys.exit(1)

    answer = query(question, sources=enabled, verbose=args.verbose)
    print(answer)


if __name__ == "__main__":
    main()
