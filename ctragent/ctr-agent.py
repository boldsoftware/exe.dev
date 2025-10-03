#!/usr/bin/env python3
import argparse
import os
import random
import secrets
import subprocess
import sys
from pathlib import Path


ADJECTIVES = [
    "happy", "clever", "brave", "calm", "eager", "gentle", "jolly", "kind",
    "lively", "proud", "swift", "wise", "bright", "cool", "fair", "keen",
    "noble", "quick", "sharp", "warm", "bold", "daring", "fuzzy", "silly"
]

ANIMALS = [
    "ant", "bear", "cat", "dog", "eagle", "fox", "goat", "hawk", "ibex",
    "jay", "koala", "lion", "mouse", "newt", "owl", "panda", "quail", "rabbit",
    "seal", "tiger", "urchin", "viper", "wolf", "yak", "zebra", "otter", "penguin"
]


def inside_mode(args):
    """Run inside the container - setup worktree and start agent."""
    # Create tmux session first if we need tsnsrv
    if args.ts_authkey:
        # Create detached tmux session
        subprocess.run(["tmux", "new-session", "-d", "-s", "mysession"], check=True)

        # Start tsnsrv in a separate pane
        tsnsrv_cmd = f'TS_AUTHKEY={args.ts_authkey} /go/bin/tsnsrv -name {args.slug} -listenAddr :9000 -plaintext=true http://0.0.0.0:9000/'
        subprocess.run(
            ["tmux", "new-window", "-t", "mysession", "-n", "tsnsrv", tsnsrv_cmd],
            check=True
        )
        print(f"Started tsnsrv for {args.slug} in tmux pane")

    # Fix ownership
    whoami = subprocess.run(["whoami"], capture_output=True, text=True, check=True).stdout.strip()
    subprocess.run(["sudo", "chown", "-R", whoami, os.getcwd()], check=True)

    # Create unique work directory with random suffix
    random_suffix = secrets.token_hex(8)
    unique_work_dir = f"/home/agent/work-{random_suffix}"
    os.mkdir(unique_work_dir)

    # Add worktree to the unique directory
    # subprocess.run("bash")
    subprocess.run(
        ["git", "worktree", "add", unique_work_dir, "-b", args.slug, args.committish],
        # I don't know why this is necessary:
        cwd=args.git_dir + "/.git",
        check=True
    )

    # Rename the unique directory to /home/agent/work
    os.rename(unique_work_dir, "/home/agent/work")
    Path(unique_work_dir).symlink_to("/home/agent/work")

    # Change to work directory and then to prefix directory
    os.chdir("/home/agent/work")
    os.chdir(args.prefix)

    # Create symlink for .claude.json to work around directory-only mount limitation
    claude_json_symlink = Path("/home/agent/.claude.json")
    if not claude_json_symlink.exists():
        claude_json_symlink.symlink_to("/home/agent/.claude/claude.json")

    # Start or attach to tmux session with appropriate agent
    if args.agent == "codex":
        agent_cmd = "codex -s danger-full-access"
        if args.prompt:
            agent_cmd += f" {args.prompt}"
    elif args.agent == "happy":
        agent_cmd = "happy --yolo"
    elif args.agent == "claude":
        agent_cmd = "claude --dangerously-skip-permissions"
    elif args.agent == "shelley":
        agent_cmd = "/mnt/shelley serve"
    elif args.agent == "bash":
        cmd = ["bash"]
        subprocess.run(cmd, check=False)
        # Early return for bash - no tmux needed
        print(f"\nExited container: {args.slug}")
        return
    elif args.agent == "tmux":
        agent_cmd = "bash"
    else:
        raise Error("unrecognized agent: " + args.agent)

    # If tmux session already exists (from tsnsrv), attach to it and run agent in main window
    # Otherwise create new session
    if args.ts_authkey:
        cmd = ["tmux", "attach-session", "-t", "mysession"]
        # Set up the first window to run the agent
        subprocess.run(
            ["tmux", "send-keys", "-t", "mysession:0", agent_cmd, "Enter"],
            check=True
        )
    else:
        cmd = ["tmux", "new-session", "-s", "mysession", agent_cmd]

    subprocess.run(cmd, check=False)

    # After exit, print slug and clean up worktree if branch hasn't moved
    print(f"\nExited container: {args.slug}")

    # Check if branch still points to original commit
    current_commit = subprocess.run(
        ["git", "rev-parse", args.slug],
        capture_output=True, text=True, check=False,
        cwd=args.git_dir
    )

    if current_commit.returncode == 0 and current_commit.stdout.strip() == args.committish:
        print(f"Branch {args.slug} unchanged, cleaning up...")
        subprocess.run(
            ["git", "worktree", "remove", "--force", unique_work_dir],
            cwd=args.git_dir,
            check=False
        )
        subprocess.run(
            ["git", "worktree", "prune"],
            cwd=args.git_dir,
            check=False
        )
        subprocess.run(
            ["git", "branch", "-D", args.slug],
            cwd=args.git_dir,
            check=False
        )
    else:
        print(f"Branch {args.slug} has moved, keeping worktree and branch")


def generate_random_slug():
    """Generate a random two-word hyphenated slug."""
    adjective = random.choice(ADJECTIVES)
    animal = random.choice(ANIMALS)
    return f"{adjective}-{animal}"


def outside_mode(args):
    """Run outside the container - setup and launch docker."""
    # Generate slug if not provided
    if not args.slug:
        args.slug = generate_random_slug()
        print(f"Generated slug: {args.slug}")

    # Get git information
    git_dir = subprocess.run(
        ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
        capture_output=True, text=True, check=True
    ).stdout.strip()

    # I don't really know why we need this, but we seem to, otherwise worktrees
    # have a dirty "status" when you create them
    if git_dir.endswith(".git"):
        git_dir = os.path.dirname(git_dir)

    committish = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True
    ).stdout.strip()

    prefix = subprocess.run(
        ["git", "rev-parse", "--show-prefix"],
        capture_output=True, text=True, check=True
    ).stdout.strip()

    if not prefix:
        prefix = "."

    workdir = f"/home/agent"

    print(f"Git dir: {git_dir}")
    print(f"Workdir:   {workdir}")
    print(f"Committish: {committish}")

    # Get script path
    script_path = Path(__file__).resolve()

    # Build docker command
    home = os.environ.get("HOME", "")
    image_tag = "container-agent:dev"

    if args.agent == "shelley":
        subprocess.check_call(["make", "build-linux-aarch64"], cwd="/Users/philip/src/exe/shelley")

    ts_authkey = os.environ.get('TS_AUTHKEY', '')
    if not ts_authkey:
        print("WARNING: no TS_AUTHKEY set")

    docker_cmd = [
        "docker", "run", "--rm", "-it",
        "--hostname", args.slug,
        "--name", args.slug,
        "-p", "0:9000",
        "-e", f"OPENAI_API_KEY={os.environ.get('OPENAI_API_KEY', '')}",
        "-e", f"ANTHROPIC_API_KEY={os.environ.get('ANTHROPIC_API_KEY', '')}",
        "-e", f"FIREWORKS_API_KEY={os.environ.get('FIREWORKS_API_KEY', '')}",
        "-e", f"COMMITTISH={committish}",
        "-v", "/var/run/docker.sock:/var/run/docker.sock",
        "-v", f"{home}/src/ssh-sketch:/home/agent/.codex",
        "-v", f"{home}/src/ssh-sketch:/home/agent/.ssh",
        "-v", f"{home}/.claude-container:/home/agent/.claude",
        "-v", f"{home}/.happy-container:/home/agent/.happy",
        "-v", f"{home}/src/bold:/mnt/bold",
        "-v", f"{git_dir}:{git_dir}",
        "-v", f"{script_path}:/mnt/ctr-agent.py",
        "-v", "/Users/philip/src/exe/shelley/bin/shelley-linux-aarch64:/mnt/shelley",
        "-w", workdir,
        image_tag,
        "python3", "/mnt/ctr-agent.py", "inside",
        "--slug", args.slug,
        "--git-dir", git_dir,
        "--committish", committish,
        "--prefix", prefix,
        "--agent", args.agent,
    ]

    if ts_authkey:
        docker_cmd.extend(["--ts-authkey", ts_authkey])

    if args.prompt:
        docker_cmd.extend(["--prompt", args.prompt])

    subprocess.run(docker_cmd, check=False)
    print(f"\nExited container: {args.slug}")


def main():
    # Check if running in inside mode
    if len(sys.argv) > 1 and sys.argv[1] == "inside":
        # Inside mode parser
        parser = argparse.ArgumentParser(description="Run inside container")
        parser.add_argument("mode", help="Must be 'inside'")
        parser.add_argument("--git-dir", required=True, help="Git directory path")
        parser.add_argument("--committish", required=True, help="Git commit hash")
        parser.add_argument("--prefix", required=True, help="Working directory prefix")
        parser.add_argument("--agent", required=True, help="Agent to run (codex, happy, claude, bash)")
        parser.add_argument("--prompt", default="", help="Prompt for agent")
        parser.add_argument("--slug", help="slug")
        parser.add_argument("--ts-authkey", default="", help="Tailscale authkey for tsnsrv")
        args = parser.parse_args()
        inside_mode(args)
    else:
        # Outside mode parser (default, user-facing)
        parser = argparse.ArgumentParser(description="Run agent in container")
        parser.add_argument("agent", help="Agent to run (codex, happy, claude, shelley, bash)")
        parser.add_argument("slug", nargs="?", default="", help="branch name (optional, will be randomly generated if not provided)")
        parser.add_argument("prompt", nargs="?", default="", help="Prompt for agent")
        args = parser.parse_args()
        outside_mode(args)


if __name__ == "__main__":
    main()
