#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["dspy>=2.6", "python-dotenv"]
# ///
"""Daily newsletter: what happened with our users today?

Connects GitHub, Discord, and Missive data sources through the
proxy/sandbox/RLM pipeline, then asks an agent to explore all sources
and synthesize a newsletter.

Usage:
    uv run python3 panopticon/newsletter.py                     # dry-run (stdout only)
    uv run python3 panopticon/newsletter.py --dry-run --verbose  # explicit dry-run with RLM traces
    uv run python3 panopticon/newsletter.py --once --post        # single run, post to Slack, exit
    uv run python3 panopticon/newsletter.py --post               # daemon loop, posts daily at 6am Pacific

Reads panopticon/.env automatically (python-dotenv).
"""

import argparse
import logging
import os
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
from panopticon.newsletter_prompts import (
    DISCORD_DESC,
    GITHUB_DESC,
    MISSIVE_DESC,
    SIGNATURE_INSTRUCTION,
)
from panopticon.proxy import ProxyRegistry
from panopticon.sources.discord import DiscordClient, DiscordSource
from panopticon.sources.github import GitHubClient, GitHubRepo
from panopticon.sources.missive import MissiveClient, MissiveSource
from deps.dspy.predict.rlm import RLM

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("newsletter")

SLACK_CHANNEL = "news"


# ---------------------------------------------------------------------------
# Warning collector — surfaces host-side source warnings to the agent
# ---------------------------------------------------------------------------

class WarningCollector(logging.Handler):
    """Captures WARNING+ log records from source modules.

    Install on the ``panopticon.sources`` logger so that proxy-layer
    warnings (e.g. Missive batch-fetch returning 0 bodies) are visible
    to the RLM agent via the ``get_pipeline_warnings`` tool.
    """

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
    # Import from the scripts/slack package that daily_brief.py also uses.
    slack_dir = os.path.join(os.path.dirname(__file__), "..", "scripts", "slack")
    if slack_dir not in sys.path:
        sys.path.insert(0, slack_dir)

    from client import SlackClient, ensure_token

    hostname = socket.gethostname()
    footer = f"\n\n_posted from `{hostname}` · panopticon/newsletter.py_"
    warning = "_untrusted user content, take caution with links_\n\n"

    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(SLACK_CHANNEL)
    slack.post_message(
        channel_id,
        warning + text + footer,
        mrkdwn=True,
        unfurl_links=False,
        unfurl_media=False,
    )
    log.info("Posted to #%s", SLACK_CHANNEL)


# ---------------------------------------------------------------------------
# Generation
# ---------------------------------------------------------------------------

SOURCE_DESCS = {"github": GITHUB_DESC, "discord": DISCORD_DESC, "missive": MISSIVE_DESC}
VALID_SOURCES = set(SOURCE_DESCS)


def generate(args) -> str:
    """Run the RLM pipeline and return the newsletter text."""
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

    _require_env("ANTHROPIC_API_KEY")  # dspy reads it directly

    # Capture source-layer warnings so the agent can report them.
    warning_collector = WarningCollector()
    warning_collector.setFormatter(logging.Formatter("%(name)s: %(message)s"))
    logging.getLogger("panopticon.sources").addHandler(warning_collector)

    registry = ProxyRegistry()
    sig_fields: dict = {}
    call_kwargs: dict = {}

    # --- GitHub ---
    if "github" in enabled_sources:
        gh_token = _require_env("EXE_GITHUB_TOKEN")
        gh_owner = _require_env("EXE_GITHUB_OWNER")
        gh_repo = _require_env("EXE_GITHUB_REPO")
        gh_client = GitHubClient(gh_token)
        github = GitHubRepo(gh_client, gh_owner, gh_repo)
        registry.register(github)
        sig_fields["github"] = dspy.InputField(desc=GITHUB_DESC)
        call_kwargs["github"] = github

    # --- Discord ---
    if "discord" in enabled_sources:
        discord_token = _require_env("EXE_DISCORD_BOT_TOKEN")
        discord_guild_id = os.environ.get("EXE_DISCORD_GUILD_ID", "")
        discord_client = DiscordClient(discord_token)
        if discord_guild_id:
            guild_name = discord_guild_id
            try:
                guilds = discord_client.list_guilds()
                for g in guilds:
                    if g["id"] == discord_guild_id:
                        guild_name = g["name"]
                        break
            except Exception as e:
                log.warning("Could not fetch guild name: %s", e)
            discord = DiscordSource(discord_client, discord_guild_id, guild_name)
        else:
            guilds = discord_client.list_guilds()
            if not guilds:
                print("Error: Discord bot is not in any guilds.", file=sys.stderr)
                sys.exit(1)
            g = guilds[0]
            discord = DiscordSource(discord_client, g["id"], g.get("name", "Unknown"))
            log.info("Auto-selected Discord guild: %s", g.get("name"))
        registry.register(discord)
        sig_fields["discord"] = dspy.InputField(desc=DISCORD_DESC)
        call_kwargs["discord"] = discord

    # --- Missive ---
    if "missive" in enabled_sources:
        missive_token = _require_env("EXE_MISSIVE_API_KEY")
        missive_client = MissiveClient(missive_token)
        missive = MissiveSource(missive_client)
        registry.register(missive)
        sig_fields["missive"] = dspy.InputField(desc=MISSIVE_DESC)
        call_kwargs["missive"] = missive

    # --- sources description for the prompt ---
    source_names = [s.capitalize() for s in sorted(enabled_sources)]
    if len(source_names) == 1:
        sources_desc = source_names[0]
    elif len(source_names) == 2:
        sources_desc = f"{source_names[0]} and {source_names[1]}"
    else:
        sources_desc = ", ".join(source_names[:-1]) + f", and {source_names[-1]}"

    log.info("Enabled sources: %s", sources_desc)

    log.info("Starting proxy server...")
    with MuxServer(registry) as mux:
        log.info("MuxServer on %s", mux.socket_path)

        lm = dspy.LM("anthropic/claude-opus-4-6", max_tokens=16384)
        dspy.configure(lm=lm)

        now = datetime.now(timezone.utc)
        date_str = now.strftime("%Y-%m-%d")
        time_str = now.strftime("%H:%M UTC")

        sig_fields["newsletter"] = dspy.OutputField()

        instruction = SIGNATURE_INSTRUCTION.format(
            date_str=date_str,
            time_str=time_str,
            sources_desc=sources_desc,
        )
        def get_pipeline_warnings() -> list[str]:
            """Return host-side warnings from data sources (API errors, fallbacks, etc.)."""
            return warning_collector.get_warnings()

        tools = [get_pipeline_warnings]

        if not args.post:
            instruction += (
                "\n\nAt the end of the newsletter, add a short section noting "
                "any issues you hit while gathering data (API errors, missing "
                "attributes, sources that were unreachable, etc.). Call "
                "get_pipeline_warnings() to see host-side warnings from the "
                "data-source layer that aren't visible in REPL output. This "
                "helps the team debug the data pipeline."
            )

        newsletter_sig = dspy.Signature(sig_fields, instruction)

        rlm = RLM(
            newsletter_sig,
            max_iterations=20,
            max_llm_calls=50,
            uds_path=mux.socket_path,
            verbose=args.verbose,
            tools=tools,
        )

        log.info("Running RLM agent...")
        result = rlm(**call_kwargs)
        return result.newsletter


# ---------------------------------------------------------------------------
# Daemon loop
# ---------------------------------------------------------------------------

PACIFIC = ZoneInfo("America/Los_Angeles")


def main_loop(args):
    """Production loop: generate and post once daily at 6am Pacific."""
    log.info("newsletter daemon starting")
    log.info("  target: 06:00 %s", PACIFIC)
    log.info("  channel: #%s", SLACK_CHANNEL)
    log.info("  stop: touch stop")

    last_brief_date = None

    while True:
        if os.path.exists("stop"):
            os.remove("stop")
            log.info("stop file found, exiting")
            break

        now = datetime.now(PACIFIC)

        if 6 <= now.hour < 7 and last_brief_date != now.date():
            log.info("generating newsletter for %s", now.date())
            try:
                newsletter = generate(args)
                post_to_slack(newsletter)
                last_brief_date = now.date()
                log.info("posted newsletter for %s", last_brief_date)
            except Exception:
                log.exception("newsletter failed, will retry next loop")

        time.sleep(60)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).resolve().parent / ".env")

    parser = argparse.ArgumentParser(description="Daily user-pulse newsletter")
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
        default="github,discord,missive",
        help="Comma-separated sources to include (default: github,discord,missive)",
    )
    args = parser.parse_args()

    if args.post and not os.environ.get("EXE_SLACK_BOT_TOKEN", "").strip():
        print("Error: EXE_SLACK_BOT_TOKEN must be set for --post. See panopticon/.env.example.", file=sys.stderr)
        sys.exit(1)

    if args.post and not args.once and not args.dry_run:
        # Daemon mode: --post without --once
        try:
            main_loop(args)
        except KeyboardInterrupt:
            log.info("interrupted")
    else:
        # Single-shot: dry-run (default) or --once --post
        newsletter = generate(args)

        print("\n" + "=" * 70)
        print(newsletter)
        print("=" * 70 + "\n")

        if args.post:
            post_to_slack(newsletter)


if __name__ == "__main__":
    main()
