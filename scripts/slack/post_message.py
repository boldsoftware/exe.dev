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


def main() -> None:
    args = parse_args()
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(args.channel)
    message = read_message(args)
    slack.post_message(channel_id, message, mrkdwn=args.markdown)
    print(f"posted message to #{args.channel}")


if __name__ == "__main__":
    try:
        main()
    except SlackError as exc:
        print(f"slack error: {exc}", file=sys.stderr)
        raise SystemExit(1)
