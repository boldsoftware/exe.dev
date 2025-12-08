#!/usr/bin/env python3
"""
Notify Slack about deployment status.

Usage:
  # Start a deployment (posts message, prints ts to stdout):
  uv run scripts/slack/deploy_notify.py start --service exed --sha abc123 --deployer josh [--dirty]

  # Complete a deployment (adds checkmark emoji):
  uv run scripts/slack/deploy_notify.py complete --ts 1234567890.123456

  # Mark deployment as failed (adds X emoji):
  uv run scripts/slack/deploy_notify.py fail --ts 1234567890.123456
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from datetime import datetime, timezone
from zoneinfo import ZoneInfo

from client import SlackClient, SlackError, ensure_token

CHANNEL = "ship"


def get_commit_summary(sha: str) -> str:
    """Get the first line of the commit message for the given SHA."""
    try:
        result = subprocess.run(
            ["git", "log", "-1", "--format=%s", sha],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except (subprocess.TimeoutExpired, FileNotFoundError):
        pass
    return ""


def format_start_blocks(
    service: str, sha: str, deployer: str, dirty: bool, commit_msg: str
) -> list:
    """Format the deployment start message as Slack blocks."""
    now = datetime.now(timezone.utc)
    pacific = now.astimezone(ZoneInfo("America/Los_Angeles"))
    eastern = now.astimezone(ZoneInfo("America/New_York"))

    pt_str = pacific.strftime("%I:%M%p").lstrip("0").lower() + " PT"
    et_str = eastern.strftime("%I:%M%p").lstrip("0").lower() + " ET"
    utc_str = now.strftime("%H:%M UTC")

    blocks = []

    if dirty:
        blocks.append(
            {
                "type": "section",
                "text": {
                    "type": "mrkdwn",
                    "text": "*:warning: DIRTY WORKTREE :warning:*",
                },
            }
        )

    # Main info section with fields (2 per row)
    sha_url = f"https://github.com/boldsoftware/exe/commit/{sha}"
    fields = [
        {"type": "mrkdwn", "text": f"*Service*\n{service}"},
        {"type": "mrkdwn", "text": f"*SHA*\n<{sha_url}|`{sha}`>"},
        {"type": "mrkdwn", "text": f"*Who*\n{deployer}"},
        {"type": "mrkdwn", "text": f"*Time*\n{pt_str} / {et_str} / {utc_str}"},
    ]

    if commit_msg:
        # Adding a 5th field makes it span the full width on its own row
        fields.append({"type": "mrkdwn", "text": f"*Commit*\n{commit_msg}"})

    blocks.append(
        {
            "type": "section",
            "fields": fields,
        }
    )

    return blocks


def cmd_start(args: argparse.Namespace) -> None:
    """Post deployment start message and print the message timestamp."""
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(CHANNEL)

    commit_msg = get_commit_summary(args.sha)
    blocks = format_start_blocks(
        args.service, args.sha, args.deployer, args.dirty, commit_msg
    )
    # text is fallback for notifications/accessibility
    fallback = f"Deploying {args.service} {args.sha} ({args.deployer})"
    ts = slack.post_message(channel_id, fallback, blocks=blocks)

    # Print ts to stdout so caller can use it later
    print(ts)


def cmd_complete(args: argparse.Namespace) -> None:
    """Add checkmark emoji to the deployment message."""
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(CHANNEL)

    slack.add_reaction(channel_id, args.ts, "white_check_mark")


def cmd_fail(args: argparse.Namespace) -> None:
    """Add X emoji to the deployment message."""
    token = ensure_token()
    slack = SlackClient(token)
    channel_id = slack.find_channel_id(CHANNEL)

    slack.add_reaction(channel_id, args.ts, "x")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Notify Slack about deployments.")
    subparsers = parser.add_subparsers(dest="command", required=True)

    # start command
    start_parser = subparsers.add_parser("start", help="Post deployment start message")
    start_parser.add_argument(
        "--service", required=True, help="Service being deployed (exed, exelet, etc)"
    )
    start_parser.add_argument(
        "--sha", required=True, help="Short git SHA being deployed"
    )
    start_parser.add_argument(
        "--deployer", required=True, help="Who is deploying (result of whoami)"
    )
    start_parser.add_argument(
        "--dirty", action="store_true", help="Mark worktree as dirty"
    )

    # complete command
    complete_parser = subparsers.add_parser(
        "complete", help="Mark deployment as complete"
    )
    complete_parser.add_argument(
        "--ts", required=True, help="Message timestamp from start command"
    )

    # fail command
    fail_parser = subparsers.add_parser("fail", help="Mark deployment as failed")
    fail_parser.add_argument(
        "--ts", required=True, help="Message timestamp from start command"
    )

    return parser.parse_args()


def main() -> None:
    args = parse_args()

    if args.command == "start":
        cmd_start(args)
    elif args.command == "complete":
        cmd_complete(args)
    elif args.command == "fail":
        cmd_fail(args)


if __name__ == "__main__":
    try:
        main()
    except SlackError as exc:
        print(f"slack error: {exc}", file=sys.stderr)
        raise SystemExit(1)
