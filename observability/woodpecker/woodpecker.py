#!/usr/bin/env python3
"""Woodpecker: daily observability report runner.

Invokes shelley to analyze ClickHouse logs and Prometheus metrics,
then waits for the conversation to complete.

Usage:
    woodpecker.py --state-dir /path/to/state
"""

import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROMPT_FILE = os.path.join(SCRIPT_DIR, "prompt.md")
TIMEOUT = 600  # 10 minutes
RECIPIENT = "philip.zeyliger@gmail.com"
EMAIL_URL = "http://169.254.169.254/gateway/email/send"


def log(msg: str) -> None:
    print(f"[woodpecker] {msg}", flush=True)


def send_failure_email(error: str) -> None:
    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    payload = json.dumps({
        "to": RECIPIENT,
        "subject": f"\U0001fab6 Woodpecker FAILED \u2014 {today}",
        "body": f"Woodpecker failed to start a Shelley conversation.\n\nError: {error}",
    })
    try:
        subprocess.run(
            ["curl", "-s", "-X", "POST", EMAIL_URL,
             "-H", "Content-Type: application/json", "-d", payload],
            timeout=30,
        )
    except Exception:
        pass


def main() -> int:
    parser = argparse.ArgumentParser(description="Woodpecker daily observability report")
    parser.add_argument(
        "--state-dir",
        required=True,
        help="Directory for persistent state (learnings, reports, etc.)",
    )
    args = parser.parse_args()

    state_dir = os.path.abspath(args.state_dir)
    os.makedirs(state_dir, exist_ok=True)

    now = datetime.now(timezone.utc)
    log(f"Starting Woodpecker run at {now.isoformat()}")
    log(f"State directory: {state_dir}")

    if not os.path.isfile(PROMPT_FILE):
        log(f"ERROR: prompt.md not found at {PROMPT_FILE}")
        return 1

    with open(PROMPT_FILE) as f:
        prompt = f.read()

    # Substitute the state directory path into the prompt
    prompt = prompt.replace("{{STATE_DIR}}", state_dir)

    date_context = (
        f"\n\nToday is {now.strftime('%Y-%m-%d')} ({now.strftime('%A')}). "
        f"Current UTC time is {now.strftime('%H:%M:%S')}.\n"
        f"Run this analysis now."
    )
    full_prompt = prompt + date_context

    log("Sending prompt to Shelley...")
    try:
        result = subprocess.run(
            ["shelley", "client", "chat",
             "-model", "claude-opus-4.6",
             "-p", full_prompt,
             "-cwd", SCRIPT_DIR],
            capture_output=True, text=True, timeout=30,
        )
        output = result.stdout + result.stderr
    except subprocess.TimeoutExpired:
        log("ERROR: shelley client chat timed out")
        send_failure_email("shelley client chat timed out")
        return 1

    try:
        data = json.loads(output)
        conv_id = data.get("conversation_id", "")
    except (json.JSONDecodeError, TypeError):
        conv_id = ""

    if not conv_id:
        log(f"ERROR: Failed to start conversation. Output: {output}")
        send_failure_email(output)
        return 1

    log(f"Conversation started: {conv_id}")
    log(f"Waiting for completion (timeout: {TIMEOUT}s)...")

    start = time.monotonic()
    while True:
        elapsed = time.monotonic() - start
        if elapsed >= TIMEOUT:
            log(f"Agent timed out after {TIMEOUT}s.")
            break

        try:
            check = subprocess.run(
                ["shelley", "client", "list", "-limit", "20"],
                capture_output=True, text=True, timeout=15,
            )
            for line in check.stdout.splitlines():
                if conv_id in line:
                    try:
                        info = json.loads(line)
                        if not info.get("working", True):
                            log(f"Agent completed after {int(elapsed)}s.")
                            log(f"Conversation: {conv_id}")
                            log(f"Woodpecker run finished at {datetime.now(timezone.utc).isoformat()}")
                            return 0
                    except (json.JSONDecodeError, TypeError):
                        pass
        except Exception:
            pass

        time.sleep(15)

    log(f"Conversation: {conv_id}")
    log(f"Woodpecker run finished at {datetime.now(timezone.utc).isoformat()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
