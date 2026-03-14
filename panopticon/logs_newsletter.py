#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6", "python-dotenv"]
# ///
"""Logs analyzer: explore and summarize ClickHouse log data.

Connects ClickHouse, GitHub, and worktree data sources through the
proxy/sandbox/RLM pipeline, then asks an agent to explore logs, correlate
with recent code changes and PRs, and produce a summary.

Usage:
    uv run python3 panopticon/logs_newsletter.py                    # dry-run (stdout only)
    uv run python3 panopticon/logs_newsletter.py --verbose          # with RLM traces
    uv run python3 panopticon/logs_newsletter.py --once --post      # single run, post to Slack, exit
    uv run python3 panopticon/logs_newsletter.py --post             # daemon loop, posts daily at 6am Pacific
    uv run python3 panopticon/logs_newsletter.py --sources clickhouse  # ClickHouse only

Reads panopticon/.env automatically (python-dotenv).
"""

import argparse
import logging
import os
from contextlib import nullcontext
from pathlib import Path
import socket
import sys
import threading
import time
from datetime import datetime, timezone
from zoneinfo import ZoneInfo

# Project root on path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import dspy

from panopticon.mux import MuxServer
from panopticon.logs_newsletter_prompts import (
    CLICKHOUSE_DESC,
    CODE_DESC,
    COMMITS_DESC,
    GITHUB_DESC,
    SIGNATURE_INSTRUCTION,
    SOURCE_DESCS,
)
from panopticon.proxy import ProxyRegistry
from panopticon.sources.clickhouse import ClickHouseClient, ClickHouseSource
from deps.dspy.predict.rlm import RLM

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("logs")

SLACK_CHANNEL = "news"
VALID_SOURCES = set(SOURCE_DESCS)


# ---------------------------------------------------------------------------
# Warning collector — surfaces host-side source warnings to the agent
# ---------------------------------------------------------------------------

class WarningCollector(logging.Handler):
    """Captures WARNING+ log records from source modules."""

    def __init__(self):
        super().__init__(level=logging.WARNING)
        self._records: list[str] = []
        self._lock = threading.Lock()

    def emit(self, record: logging.LogRecord) -> None:
        with self._lock:
            self._records.append(self.format(record))

    def get_warnings(self) -> list[str]:
        with self._lock:
            return list(self._records)


def _require_env(name: str) -> str:
    """Read an env var, fail fast with a clear message if missing."""
    val = os.environ.get(name, "").strip()
    if not val:
        print(f"Error: {name} must be set. See panopticon/.env.example.", file=sys.stderr)
        sys.exit(1)
    return val


# ---------------------------------------------------------------------------
# Slack
# ---------------------------------------------------------------------------

def post_to_slack(text: str) -> None:
    slack_dir = os.path.join(os.path.dirname(__file__), "..", "scripts", "slack")
    if slack_dir not in sys.path:
        sys.path.insert(0, slack_dir)

    from client import SlackClient, ensure_token

    hostname = socket.gethostname()
    footer = f"\n\n_posted from `{hostname}` · panopticon/logs_newsletter.py_"

    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(SLACK_CHANNEL)
    slack.post_message(
        channel_id,
        text + footer,
        mrkdwn=True,
        unfurl_links=False,
        unfurl_media=False,
    )
    log.info("Posted to #%s", SLACK_CHANNEL)


# ---------------------------------------------------------------------------
# Generation
# ---------------------------------------------------------------------------

def generate(args) -> str:
    """Run the RLM pipeline and return the analysis text."""
    # --- parse and validate --sources ---
    enabled_sources = {s.strip().lower() for s in args.sources.split(",")}
    enabled_sources.discard("")
    unknown = enabled_sources - VALID_SOURCES
    if unknown:
        print(f"Error: unknown source(s): {', '.join(sorted(unknown))}. "
              f"Valid: {', '.join(sorted(VALID_SOURCES))}", file=sys.stderr)
        sys.exit(1)
    if not enabled_sources:
        print("Error: at least one source must be enabled via --sources.", file=sys.stderr)
        sys.exit(1)

    _require_env("ANTHROPIC_API_KEY")

    # Capture source-layer warnings so the agent can report them.
    warning_collector = WarningCollector()
    warning_collector.setFormatter(logging.Formatter("%(name)s: %(message)s"))
    logging.getLogger("panopticon.sources").addHandler(warning_collector)

    registry = ProxyRegistry()
    sig_fields: dict = {}
    call_kwargs: dict = {}
    tools: list = []
    needs_proxy = False

    # --- ClickHouse (proxy-based) ---
    if "clickhouse" in enabled_sources:
        needs_proxy = True
        ch_url = _require_env("EXE_CLICKHOUSE_URL")
        ch_password = _require_env("EXE_CLICKHOUSE_PASSWORD")
        ch_user = os.environ.get("EXE_CLICKHOUSE_USER", "readonly").strip() or "readonly"
        ch_database = os.environ.get("EXE_CLICKHOUSE_DATABASE", "").strip() or None

        ch_client = ClickHouseClient(ch_url, user=ch_user, password=ch_password)
        clickhouse = ClickHouseSource(ch_client, database=ch_database)
        registry.register(clickhouse)
        sig_fields["clickhouse"] = dspy.InputField(desc=CLICKHOUSE_DESC)
        call_kwargs["clickhouse"] = clickhouse

    # --- GitHub (proxy-based) ---
    if "github" in enabled_sources:
        from panopticon.sources.github import GitHubClient, GitHubRepo

        needs_proxy = True
        gh_token = _require_env("EXE_GITHUB_TOKEN")
        gh_owner = _require_env("EXE_GITHUB_OWNER")
        gh_repo = _require_env("EXE_GITHUB_REPO")
        gh_client = GitHubClient(gh_token)
        github = GitHubRepo(gh_client, gh_owner, gh_repo)
        registry.register(github)
        sig_fields["github"] = dspy.InputField(desc=GITHUB_DESC)
        call_kwargs["github"] = github

    # --- Worktree (direct loading, classic RLM) ---
    if "worktree" in enabled_sources:
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

    # --- sources description for the prompt ---
    source_names = sorted(enabled_sources)
    descs = [SOURCE_DESCS[s] for s in source_names]
    if len(descs) == 1:
        sources_desc = descs[0]
    elif len(descs) == 2:
        sources_desc = f"{descs[0]} and {descs[1]}"
    else:
        sources_desc = ", ".join(descs[:-1]) + f", and {descs[-1]}"

    log.info("Enabled sources: %s", ", ".join(source_names))

    if needs_proxy:
        mux_ctx = MuxServer(registry)
    else:
        mux_ctx = nullcontext()

    log.info("Starting proxy server...")
    with mux_ctx as mux:
        if mux:
            log.info("MuxServer on %s", mux.socket_path)

        lm = dspy.LM("anthropic/claude-opus-4-6", max_tokens=16384)
        dspy.configure(lm=lm)

        now = datetime.now(timezone.utc)
        date_str = now.strftime("%Y-%m-%d")
        time_str = now.strftime("%H:%M UTC")

        instruction = SIGNATURE_INSTRUCTION.format(
            date_str=date_str,
            time_str=time_str,
            sources_desc=sources_desc,
        )

        def get_pipeline_warnings() -> list[str]:
            """Return host-side warnings from data sources (API errors, fallbacks, etc.)."""
            return warning_collector.get_warnings()

        tools.append(get_pipeline_warnings)

        if not args.post:
            instruction += (
                "\n\nAt the end of the report, add a short section noting "
                "any issues you hit while gathering data (API errors, missing "
                "tables, ClickHouse errors, etc.). Call "
                "get_pipeline_warnings() to see host-side warnings from the "
                "data-source layer that aren't visible in REPL output. This "
                "helps the team debug the data pipeline."
            )

        sig_fields["report"] = dspy.OutputField()
        newsletter_sig = dspy.Signature(sig_fields, instruction)

        rlm_kwargs = dict(
            max_iterations=20,
            max_llm_calls=50,
            verbose=args.verbose,
            tools=tools,
        )
        if mux:
            rlm_kwargs["uds_path"] = mux.socket_path

        rlm = RLM(newsletter_sig, **rlm_kwargs)

        log.info("Running RLM agent...")
        result = rlm(**call_kwargs)
        return result.report


# ---------------------------------------------------------------------------
# Daemon loop
# ---------------------------------------------------------------------------

PACIFIC = ZoneInfo("America/Los_Angeles")


def main_loop(args):
    """Production loop: generate and post once daily at 6am Pacific."""
    log.info("logs daemon starting")
    log.info("  target: 06:00 %s", PACIFIC)
    log.info("  channel: #%s", SLACK_CHANNEL)
    log.info("  stop: touch stop")

    last_run_date = None

    while True:
        if os.path.exists("stop"):
            os.remove("stop")
            log.info("stop file found, exiting")
            break

        now = datetime.now(PACIFIC)

        if 6 <= now.hour < 7 and last_run_date != now.date():
            log.info("generating log analysis for %s", now.date())
            try:
                report = generate(args)
                post_to_slack(report)
                last_run_date = now.date()
                log.info("posted log analysis for %s", last_run_date)
            except Exception:
                log.exception("log analysis failed, will retry next loop")

        time.sleep(60)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parent / ".env")

    parser = argparse.ArgumentParser(description="ClickHouse log analyzer")
    parser.add_argument(
        "--post", action="store_true",
        help="Post to Slack #news (default: dry-run to stdout)",
    )
    parser.add_argument(
        "--dry-run", action="store_true", default=False,
        help="Generate and print to stdout only (default when --post is not set)",
    )
    parser.add_argument(
        "--once", action="store_true",
        help="Run a single generation (and post if --post), then exit",
    )
    parser.add_argument(
        "--verbose", action="store_true",
        help="Print RLM REPL traces to stdout",
    )
    parser.add_argument(
        "--sources",
        default="clickhouse,github,worktree",
        help="Comma-separated sources to include (default: clickhouse,github,worktree)",
    )
    args = parser.parse_args()

    if args.post and not os.environ.get("EXE_SLACK_BOT_TOKEN", "").strip():
        print("Error: EXE_SLACK_BOT_TOKEN must be set for --post. See panopticon/.env.example.", file=sys.stderr)
        sys.exit(1)

    if args.post and not args.once and not args.dry_run:
        try:
            main_loop(args)
        except KeyboardInterrupt:
            log.info("interrupted")
    else:
        report = generate(args)

        print("\n" + "=" * 70)
        print(report)
        print("=" * 70 + "\n")

        if args.post:
            post_to_slack(report)


if __name__ == "__main__":
    main()
