#!/usr/bin/env python3
"""
Post a free-form message to a Slack channel using the EXE_SLACK_BOT_TOKEN.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

from client import SlackClient, SlackError, ensure_token


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Post a message to Slack.")
    parser.add_argument(
        "--channel",
        required=True,
        help="Channel name without '#'.",
    )
    parser.add_argument(
        "--markdown",
        action="store_true",
        default=None,
        help="Render the message body as Slack mrkdwn.",
    )
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument(
        "--message",
        help="Literal message text.",
    )
    group.add_argument(
        "--message-file",
        type=pathlib.Path,
        help="Read the message body from this file.",
    )
    return parser.parse_args()


def read_message(args: argparse.Namespace) -> str:
    if args.message is not None:
        return args.message
    assert args.message_file is not None
    content = args.message_file.read_text(encoding="utf-8")
    return content


THREAD_THRESHOLD = 3000


def main() -> None:
    args = parse_args()
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(args.channel)
    message = read_message(args)

    if len(message) <= THREAD_THRESHOLD:
        slack.post_message(channel_id, message, mrkdwn=args.markdown)
        print(f"posted message to #{args.channel}")
    else:
        # Long message: post summary with 🧵, then full content in thread
        lines = message.rstrip().split("\n")
        # Extract the CI logs link (last line) if present
        last_line = lines[-1] if lines else ""
        if last_line.startswith("<") and "|" in last_line and last_line.endswith(">"):
            summary = f"🧵 CI failure - see thread for details\n{last_line}"
        else:
            summary = "🧵 CI failure - see thread for details"
        parent_ts = slack.post_message(channel_id, summary, mrkdwn=args.markdown)
        slack.post_message(channel_id, message, mrkdwn=args.markdown, thread_ts=parent_ts)
        print(f"posted message with thread to #{args.channel}")


if __name__ == "__main__":
    try:
        main()
    except SlackError as exc:
        print(f"slack error: {exc}", file=sys.stderr)
        raise SystemExit(1)
