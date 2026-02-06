#!/usr/bin/env python3
"""
Check the #btdb Slack ledger and alert #oops if any CI bots look dead.

Update BOT_EXPECTATIONS whenever a workflow starts writing to #btdb so the
liveness check keeps pace with new bots.
"""

from __future__ import annotations

import argparse
import sys
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Dict, List, Mapping, Sequence

from client import SlackClient, SlackError, ensure_token
from update_bot_status import extract_json


@dataclass(frozen=True)
class BotExpectation:
    bot: str
    frequency: timedelta
    context: str


BOT_EXPECTATIONS: Sequence[BotExpectation] = (
    # BotExpectation(
    #     bot="e2e-ai",
    #     frequency=timedelta(days=7),
    #     context=".github/workflows/exe-e2e-ai-tests.yml runs weekly at 06:45 UTC on Mondays.",
    # ),
    BotExpectation(
        bot="e3e-security",
        frequency=timedelta(days=7),
        context=".github/workflows/exe-e3e-security.yml runs weekly at 07:15 UTC on Tuesdays.",
    ),
    BotExpectation(
        bot="e4e-docs",
        frequency=timedelta(days=7),
        context=".github/workflows/exe-e4e-docs.yml runs weekly at 07:45 UTC on Wednesdays.",
    ),
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Verify that CI bots continue updating the Slack #btdb ledger."
    )
    parser.add_argument(
        "--ledger-channel",
        default="btdb",
        help="Channel containing the bot ledger (without '#'). Defaults to btdb.",
    )
    parser.add_argument(
        "--alert-channel",
        default="oops",
        help="Channel for dead-bot alerts (without '#'). Defaults to oops.",
    )
    return parser.parse_args()


def build_expectation_map(specs: Sequence[BotExpectation]) -> Dict[str, BotExpectation]:
    return {spec.bot: spec for spec in specs}


def fetch_recent_entries(
    slack: SlackClient,
    channel_id: str,
    target_bots: Mapping[str, BotExpectation],
) -> Dict[str, Dict[str, object]]:
    results: Dict[str, Dict[str, object]] = {}
    for message in slack.iter_history(channel_id):
        payload = extract_json((message.get("text") or "").strip())
        if not payload:
            continue
        bot = payload.get("bot")
        if not isinstance(bot, str):
            continue
        if bot not in target_bots:
            continue
        if bot in results:
            continue
        results[bot] = payload
        if len(results) == len(target_bots):
            break
    return results


def parse_last_run(bot: str, payload: Mapping[str, object]) -> datetime:
    raw = payload.get("last_run")
    if not isinstance(raw, str) or not raw:
        raise RuntimeError(
            f"#btdb entry for bot {bot!r} is missing the required 'last_run' field"
        )
    try:
        parsed = datetime.fromisoformat(raw)
    except ValueError as exc:
        raise RuntimeError(
            f"#btdb entry for bot {bot!r} has invalid last_run {raw!r}"
        ) from exc
    if parsed.tzinfo is None:
        raise RuntimeError(
            f"#btdb entry for bot {bot!r} stored a naive timestamp {raw!r}"
        )
    return parsed.astimezone(timezone.utc)


@dataclass
class BotIssue:
    spec: BotExpectation
    reason: str


def evaluate_bots(
    now: datetime,
    specs: Mapping[str, BotExpectation],
    payloads: Mapping[str, Mapping[str, object]],
) -> List[BotIssue]:
    issues: List[BotIssue] = []
    for name, spec in specs.items():
        payload = payloads.get(name)
        if payload is None:
            issues.append(
                BotIssue(
                    spec=spec,
                    reason="no #btdb entry found; the workflow might not have reported status",
                )
            )
            continue
        last_run = parse_last_run(name, payload)
        age = now - last_run
        allowed = spec.frequency * 2
        if age <= allowed:
            continue
        status = payload.get("status")
        run_url = payload.get("run_url")
        details = [
            f"last seen at {last_run.isoformat()}",
            f"age {format_duration(age)} (limit {format_duration(allowed)})",
        ]
        if isinstance(status, str) and status:
            details.append(f"status {status}")
        if isinstance(run_url, str) and run_url:
            details.append(f"run {run_url}")
        issues.append(
            BotIssue(
                spec=spec,
                reason=", ".join(details),
            )
        )
    return issues


def format_duration(delta: timedelta) -> str:
    total_seconds = int(delta.total_seconds())
    if total_seconds % 86400 == 0:
        days = total_seconds // 86400
        return f"{days}d"
    if total_seconds % 3600 == 0:
        hours = total_seconds // 3600
        return f"{hours}h"
    if total_seconds % 60 == 0:
        minutes = total_seconds // 60
        return f"{minutes}m"
    return f"{total_seconds}s"


def format_alert(issues: Sequence[BotIssue]) -> str:
    lines = [
        ":rotating_light: CI liveness watchdog found stale bots:",
    ]
    for issue in issues:
        lines.append(f"- {issue.spec.bot}: {issue.reason} ({issue.spec.context})")
    lines.append("Investigate the workflows above or rerun them manually.")
    return "\n".join(lines)


def main() -> int:
    args = parse_args()
    expectations = build_expectation_map(BOT_EXPECTATIONS)
    token = ensure_token()
    slack = SlackClient(token)
    ledger_channel_id = slack.find_channel_id(args.ledger_channel)
    payloads = fetch_recent_entries(slack, ledger_channel_id, expectations)
    now = datetime.now(timezone.utc)
    issues = evaluate_bots(now, expectations, payloads)
    if not issues:
        print("All tracked bots have reported in recently.")
        return 0
    alert_channel_id = slack.find_channel_id(args.alert_channel)
    message = format_alert(issues)
    slack.post_message(alert_channel_id, message)
    print(f"Alerted #{args.alert_channel} about {len(issues)} stale bots.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SlackError as exc:
        print(f"slack error: {exc}", file=sys.stderr)
        raise SystemExit(1)
    except RuntimeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(1)
