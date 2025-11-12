#!/usr/bin/env python3
"""
Update the bot-maintained record inside Slack's #btdb channel.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import OrderedDict
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from client import SlackClient, SlackError, ensure_token


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Update the JSON record for a CI bot inside #btdb.",
    )
    parser.add_argument(
        "bot",
        help="Name of the bot (stored in the JSON payload's 'bot' field).",
    )
    parser.add_argument(
        "--status",
        help=(
            "Optional status string stored alongside the timestamp "
            "(e.g. success, failure, cancelled, running, unknown)."
        ),
    )
    parser.add_argument(
        "--run-url",
        dest="run_url",
        help="Optional URL pointing to the CI run.",
    )
    parser.add_argument(
        "--notes",
        help="Optional short notes to include in the JSON payload.",
    )
    parser.add_argument(
        "--channel",
        default="btdb",
        help="Slack channel name (without #). Defaults to btdb.",
    )
    return parser.parse_args()


def build_payload(args: argparse.Namespace) -> OrderedDict[str, Any]:
    record: "OrderedDict[str, Any]" = OrderedDict()
    record["bot"] = args.bot
    record["last_run"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    if args.status:
        record["status"] = args.status
    if args.run_url:
        record["run_url"] = args.run_url
    notes = (args.notes or "").strip()
    if notes:
        record["notes"] = notes
    return record


def format_message(payload: OrderedDict[str, Any]) -> str:
    json_text = json.dumps(payload, separators=(", ", ": "), ensure_ascii=False)
    return f"```\n{json_text}\n```"


def extract_json(text: str) -> Optional[Dict[str, Any]]:
    raw = text.strip()
    if not raw:
        return None
    candidate = raw
    if raw.startswith("```"):
        candidate = strip_code_fence(raw) or raw
    try:
        return json.loads(candidate)
    except json.JSONDecodeError:
        return None


def strip_code_fence(text: str) -> Optional[str]:
    if not text.startswith("```"):
        return None
    closing = text.rfind("```")
    if closing <= 0:
        return None
    first_newline = text.find("\n")
    if first_newline < 0 or first_newline >= closing:
        return None
    inner = text[first_newline + 1 : closing]
    return inner.strip()


def find_existing_message(slack: SlackClient, channel_id: str, bot_name: str) -> Optional[str]:
    for message in slack.iter_history(channel_id):
        text = (message.get("text") or "").strip()
        if not text:
            continue
        payload = extract_json(text)
        if payload is None:
            continue
        if payload.get("bot") == bot_name:
            ts = message.get("ts")
            if ts:
                return ts
    return None


def main() -> None:
    args = parse_args()
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(args.channel)
    payload = build_payload(args)
    body = format_message(payload)
    ts = find_existing_message(slack, channel_id, args.bot)
    if ts:
        slack.update_message(channel_id, ts, body)
        action = "updated"
    else:
        slack.post_message(channel_id, body)
        action = "posted"
    print(f"{action} #{args.channel} entry for bot {args.bot}")


if __name__ == "__main__":
    try:
        main()
    except SlackError as exc:
        print(f"slack error: {exc}", file=sys.stderr)
        raise SystemExit(1)
