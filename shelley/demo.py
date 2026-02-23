#!/usr/bin/env python3
"""Build and (re)start a Shelley demo server in a tmux session.

Port is deterministic: derived from a hash of the worktree path (3000-3999).
The tmux session is named 'shelley-demo-<port>'.

Usage:
    shelley/demo.py              # build + (re)start
    shelley/demo.py stop         # kill the tmux session
    shelley/demo.py status       # show whether it's running + URL
    shelley/demo.py port         # just print the port
    shelley/demo.py logs         # attach to the tmux session (ctrl-b d to detach)
"""
import hashlib
import os
import subprocess
import sys
import time
from pathlib import Path
from urllib.request import urlopen
from urllib.error import URLError

SCRIPT_DIR = Path(__file__).resolve().parent
SHELLEY_DIR = SCRIPT_DIR
CONFIG = "/exe.dev/shelley.json"
DB = Path.home() / ".config/shelley/shelley.db"
HOSTNAME = "phil-dev.exe.xyz"


def port_for_dir() -> int:
    h = hashlib.sha256(str(SHELLEY_DIR).encode()).hexdigest()[:8]
    return 3000 + (int(h, 16) % 1000)


def session_name(port: int) -> str:
    return f"shelley-demo-{port}"


def tmux_has_session(name: str) -> bool:
    return subprocess.run(
        ["tmux", "has-session", "-t", name],
        capture_output=True,
    ).returncode == 0


def tmux_kill_session(name: str):
    subprocess.run(["tmux", "kill-session", "-t", name], capture_output=True)


def health_check(port: int, timeout: float = 5.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            urlopen(f"http://localhost:{port}/", timeout=1)
            return True
        except (URLError, OSError):
            time.sleep(0.15)
    return False


def cmd_start(port: int):
    sess = session_name(port)
    binary = SHELLEY_DIR / "bin" / "shelley"

    # Build
    print(f"Building shelley in {SHELLEY_DIR} ...")
    subprocess.run(["make", "build"], cwd=SHELLEY_DIR, check=True)
    print("Build complete.")

    # Kill existing session
    if tmux_has_session(sess):
        print(f"Killing existing tmux session '{sess}'")
        tmux_kill_session(sess)
        time.sleep(0.3)

    # Start in tmux
    cmd = f"{binary} --config {CONFIG} --db {DB} serve --port {port}"
    print(f"Starting demo server on port {port} (tmux session '{sess}') ...")
    subprocess.run(
        ["tmux", "new-session", "-d", "-s", sess, cmd],
        check=True,
    )

    # Health check
    if health_check(port):
        print(f"Demo server running on port {port}")
    else:
        print(f"Warning: port {port} not responding yet. Check: tmux attach -t {sess}")
    print(f"URL: https://{HOSTNAME}:{port}/")


def cmd_stop(port: int):
    sess = session_name(port)
    if tmux_has_session(sess):
        tmux_kill_session(sess)
        print(f"Stopped (killed tmux session '{sess}').")
    else:
        print(f"Not running (no tmux session '{sess}').")


def cmd_status(port: int):
    sess = session_name(port)
    if tmux_has_session(sess):
        print(f"Running (tmux session '{sess}') on port {port}")
        print(f"URL: https://{HOSTNAME}:{port}/")
        print(f"Logs: tmux attach -t {sess}")
    else:
        print(f"Not running (port {port})")


def cmd_logs(port: int):
    sess = session_name(port)
    if not tmux_has_session(sess):
        print(f"Not running (no tmux session '{sess}').")
        sys.exit(1)
    os.execvp("tmux", ["tmux", "attach", "-t", sess])


def main():
    port = port_for_dir()
    action = sys.argv[1] if len(sys.argv) > 1 else "start"

    actions = {
        "start": lambda: cmd_start(port),
        "stop": lambda: cmd_stop(port),
        "status": lambda: cmd_status(port),
        "port": lambda: print(port),
        "logs": lambda: cmd_logs(port),
    }

    if action not in actions:
        print(f"Usage: {sys.argv[0]} [{'/'.join(actions)}]", file=sys.stderr)
        sys.exit(1)

    actions[action]()


if __name__ == "__main__":
    main()
