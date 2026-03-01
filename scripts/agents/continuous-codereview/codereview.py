#!/usr/bin/env python3
"""Unified codereview tooling.

Subcommands:
    read-notes    — collect all codereview notes relevant to the current branch
    write-notes   — write a codereview note on HEAD and clean up old (pre-amend) notes
    claude        — run a claude CLI review
    codex         — run a codex CLI review, extracting the agent message
    context       — print git context (branch, merge base, commits) for the current branch
    run           — run paired claude+codex review agents with merger
"""

import argparse
import json
import os
import subprocess
import sys

NOTES_REF = "codereview"

# Pre-canned role prompts, keyed by role name. When an agent config includes
# a "role", its prompt is prepended to any extra_prompt the agent may have.
AGENT_ROLES = {
    "architecture": (
        "You are dedicated to the 40,000-foot question: does this change "
        "do what it claims to, and is it the right approach? Read the diff, "
        "understand the surrounding architecture, and evaluate whether the "
        "overall direction makes sense. Consider alternatives, flag cases "
        "where the change is solving the wrong problem, solving the right "
        "problem at the wrong layer, or failing to deliver on its stated "
        "intent. Identify structural decisions that will be expensive to "
        "reverse later. You do not need to produce line-level comments — "
        "your job is strategic, not tactical."
    ),
}

DEFAULT_MERGER_PROMPT = (
    "You are merging multiple independent code reviews into a single coherent "
    "review. Read each review file carefully. Your job:\n"
    "\n"
    "1. Filter against prior decisions — if codereview notes are provided, "
    "discard reviewer items that match an existing stet before merging.\n"
    "2. Resolve disagreements — inspect the source code to determine who is "
    "correct and present only the correct conclusion.\n"
    "3. Deduplicate — if multiple reviewers flagged the same issue, consolidate "
    "into one item with the strongest analysis.\n"
    "4. Answer open questions — if a reviewer expressed uncertainty about "
    "something that can be resolved by reading the code, resolve it.\n"
    "5. Verify claims — spot-check reviewers' assertions against the actual "
    "source.\n"
    "6. If any reviewer raises a fundamental concern about overall approach or "
    "architecture, surface that prominently — do not bury it among line-level "
    "feedback.\n"
    "\n"
    "Output a single merged review, fully formatted per the review format "
    "instructions below."
)

REVIEW_CONTENT_PROMPT = (
    "The goal: everything the user needs to act, nothing they don't. The user's\n"
    "time is the scarcest resource — all easy questions, confusions, and noise must\n"
    "be resolved before the final review reaches them.\n"
    "\n"
    "**Every item gets lettered options.** The user should be able to reply with\n"
    "mostly just references — \"2a, 5c, 11b\" — to dispatch the entire review.\n"
    "\n"
    "Each item gets:\n"
    "\n"
    "- A clear statement of the issue or observation\n"
    "- Relevant diff/code snippets inline (the user shouldn't have to go look things up)\n"
    "- A set of options to pick from\n"
    "\n"
    "Options should include item-specific actions (fixes, refactors, etc.) as\n"
    "appropriate. In addition, these standard options are often relevant:\n"
    "\n"
    "- **temporarily ignore** — skip for now; not persisted, so it may be re-raised in future reviews\n"
    "- **stet** — intentional, leave as-is; persisted in codereview notes, will not be re-raised\n"
    "- **add comments** — add comments to the code to improve clarity\n"
    "- **file a follow-up** — file a GitHub issue to follow-up\n"
    "\n"
    "Adding options is cheap — err on the side of giving the user more choices\n"
    "rather than fewer. Every item, including purely informational observations and\n"
    "clarity flags, must have options.\n"
    "\n"
    "Include clarity flags: items where the code is confusing or concerning but\n"
    "determined to be correct. Note in the body that it is a clarity flag.\n"
    "\n"
    "Exclude:\n"
    "- Questions answerable by inspecting the code (resolve these yourself)\n"
    "- Commentary on good aspects of the code\n"
    "- Preamble, hedging, or filler\n"
    "\n"
    "Every line should earn its place. Optimize for SNR."
)

REVIEW_OUTPUT_FORMAT = (
    "Output format: use the tag-based format below. Each tag must appear alone on\n"
    "its own line. Content between tags is free-form text — no escaping needed.\n"
    "Do NOT number items or letter options; that is handled automatically.\n"
    "\n"
    "<item>\n"
    "Issue description with code snippets.\n"
    "<option>\n"
    "An action the user can choose\n"
    "</option>\n"
    "<option>\n"
    "Another action\n"
    "</option>\n"
    "</item>\n"
)

NOTES_PROMPT = (
    "## Prior codereview decisions\n"
    "\n"
    "Each `STET:key` entry is a deliberate decision to leave something as-is.\n"
    "Filter reviewer items against these, matching semantically.\n"
    "\n"
    "Notes:\n"
)

BASE_REVIEW_PROMPT = (
    "Review the code changes for issues, concerns, and suggestions. "
    "Include supporting code snippets and analysis. "
    "Consult surrounding code as needed to verify correctness. "
    "If a question can be answered by consulting the code, do so "
    "instead of leaving it open. "
    "Do NOT invoke the codereview skill."
)


def parse_review(text):
    """Parse faux-XML review format into structured elements.

    Returns a list of {"body": "...", "options": [{"text": "..."}, ...]}.
    Numbers and letters are assigned by the renderers, not the parser.
    """
    elements = []
    current_item = None
    lines = []
    state = "outside"  # outside | body | option | between_options

    for line in text.splitlines():
        stripped = line.strip()

        if stripped == "<item>":
            current_item = {"body": "", "options": []}
            lines = []
            state = "body"
            continue

        if stripped == "</item>" and current_item is not None:
            if state == "option":
                current_item["options"][-1]["text"] = "\n".join(lines).strip()
            elif state == "body":
                current_item["body"] = "\n".join(lines).strip()
            elements.append(current_item)
            current_item = None
            lines = []
            state = "outside"
            continue

        if stripped == "<option>":
            if state == "body":
                current_item["body"] = "\n".join(lines).strip()
            elif state == "option":
                current_item["options"][-1]["text"] = "\n".join(lines).strip()
            current_item["options"].append({})
            lines = []
            state = "option"
            continue

        if stripped == "</option>":
            if state == "option":
                current_item["options"][-1]["text"] = "\n".join(lines).strip()
            lines = []
            state = "between_options"
            continue

        if state in ("body", "option"):
            lines.append(line)

    return elements


def render_markdown(elements):
    """Render parsed elements to numbered/lettered markdown."""
    parts = []
    for i, el in enumerate(elements):
        num = i + 1
        parts.append(f"{num}. {el['body']}")
        parts.append("")
        for j, opt in enumerate(el["options"]):
            letter = chr(ord("a") + j)
            parts.append(f"  {num}{letter}. {opt['text']}")
        parts.append("")
    return "\n".join(parts)


def render_json(elements):
    """Render parsed elements to JSON-serializable dict."""
    items = []
    for i, el in enumerate(elements):
        num = i + 1
        options = []
        for j, opt in enumerate(el["options"]):
            letter = chr(ord("a") + j)
            options.append({"letter": letter, "text": opt["text"]})
        items.append({"number": num, "body": el["body"], "options": options})
    return {"items": items}


def git(*args, die=None):
    """Run a git command. Return stdout on success, None on failure.

    If die is a string and the command fails, print it with git's stderr and exit.
    """
    r = subprocess.run(
        ["git", *args],
        capture_output=True,
        text=True,
    )
    if r.returncode != 0:
        if die is not None:
            detail = r.stderr.strip()
            msg = f"error: {die}"
            if detail:
                msg += f": {detail}"
            print(msg, file=sys.stderr)
            sys.exit(1)
        return None
    return r.stdout.strip()


def get_merge_base():
    return git("merge-base", "HEAD", "origin/main")


def get_current_commits(base):
    if not base:
        return []
    result = git("log", "--format=%H", f"{base}..HEAD")
    return result.splitlines() if result else []


def get_reflog_shas():
    """Recent reflog SHAs for the current branch (catches pre-amend commits)."""
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    if not branch or branch == "HEAD":
        branch = "HEAD"
    result = git("reflog", "show", "--format=%H", "-n", "100", branch)
    return result.splitlines() if result else []


def read_note(sha):
    return git("notes", "--ref", NOTES_REF, "show", sha)


def collect_candidate_shas():
    """Deduplicated SHAs from current commits + branch reflog."""
    base = get_merge_base()
    current = get_current_commits(base)
    reflog = get_reflog_shas()
    seen = set()
    out = []
    for sha in current + reflog:
        if sha not in seen:
            seen.add(sha)
            out.append(sha)
    return out


def cleanup_old_notes(head):
    """Remove notes from old reflog SHAs now that HEAD has the consolidated note."""
    removed = 0
    for sha in get_reflog_shas():
        if sha == head:
            continue
        if read_note(sha):
            git("notes", "--ref", NOTES_REF, "remove", sha)
            removed += 1
    if removed:
        print(f"cleaned up {removed} old note(s)", file=sys.stderr)


# -- subcommands --


def cmd_read_notes(_args):
    candidates = collect_candidate_shas()
    first = True
    for sha in candidates:
        note = read_note(sha)
        if note:
            if not first:
                print()
            print(note)
            first = False


def cmd_write_notes(args):
    if args.file:
        with open(args.file) as f:
            content = f.read().strip()
    elif args.message:
        content = args.message
    else:
        content = sys.stdin.read().strip()

    if not content:
        print("error: empty note content", file=sys.stderr)
        sys.exit(1)

    head = git("rev-parse", "HEAD", die="could not resolve HEAD")
    git("notes", "--ref", NOTES_REF, "add", "-f", "HEAD", "-m", content,
        die="failed to write note")

    cleanup_old_notes(head)


def cmd_context(_args):
    base = get_merge_base()
    if not base:
        print("error: could not determine merge base", file=sys.stderr)
        sys.exit(1)
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    short_base = git("rev-parse", "--short", base)
    log = git("log", "--reverse", "--format=%h %s", f"{base}..HEAD")
    print(f"Branch: {branch}")
    print(f"Merge base: {short_base}")
    if log:
        print()
        print("Commits:")
        for line in log.splitlines():
            print(f"  {line}")


def cmd_claude(args):
    sys.exit(
        subprocess.run(
            [
                "claude",
                "--dangerously-skip-permissions",
                "--model",
                "opus",
                "-p",
                args.prompt,
            ]
        ).returncode
    )


def cmd_codex(args):
    r = subprocess.run(
        [
            "codex",
            "--dangerously-bypass-approvals-and-sandbox",
            "exec",
            "-m",
            "gpt-5.3-codex",
            "--json",
            args.prompt,
        ],
        capture_output=True,
        text=True,
    )
    if r.returncode != 0:
        print(f"error: codex exited with status {r.returncode}", file=sys.stderr)
        sys.exit(r.returncode)

    # Parse NDJSON: find the last item.completed event with item.type == "agent_message"
    last_text = None
    for line in r.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if (
            event.get("type") == "item.completed"
            and isinstance(event.get("item"), dict)
            and event["item"].get("type") == "agent_message"
        ):
            text = event["item"].get("text")
            if text is not None:
                last_text = text

    if last_text is None:
        print("error: no agent_message found in codex output", file=sys.stderr)
        sys.exit(1)
    print(last_text)


def cmd_run(args):
    review_dir = os.path.abspath(args.review_dir)

    with open(os.path.join(review_dir, "config.json")) as f:
        cfg = json.load(f)

    with open(os.path.join(review_dir, "context.txt")) as f:
        context = f.read().strip()

    if not context:
        print("error: context.txt is empty", file=sys.stderr)
        sys.exit(1)

    notes_content = ""
    notes_path = os.path.join(review_dir, "notes.txt")
    if os.path.exists(notes_path):
        with open(notes_path) as f:
            notes_content = f.read().strip()

    agents = cfg.get("agents")
    if not agents or not isinstance(agents, list):
        print("error: config requires a non-empty agents list", file=sys.stderr)
        sys.exit(1)

    # Validate agent names.
    names = []
    for agent in agents:
        if not agent.get("name"):
            print("error: each agent requires a name", file=sys.stderr)
            sys.exit(1)
        names.append(agent["name"])
    if len(names) != len(set(names)):
        print("error: agent names must be unique", file=sys.stderr)
        sys.exit(1)

    # Validate roles.
    for agent in agents:
        role = agent.get("role")
        if role and role not in AGENT_ROLES:
            print(f"error: unknown role '{role}' for {agent['name']}", file=sys.stderr)
            sys.exit(1)

    # Each logical agent spawns a claude+codex pair.
    this_script = os.path.abspath(__file__)
    devnull = open(os.devnull, "w")
    procs = []
    for agent in agents:
        prompt = context + "\n\n" + BASE_REVIEW_PROMPT
        role = agent.get("role")
        if role:
            prompt += "\n\n" + AGENT_ROLES[role]
        if agent.get("extra_prompt"):
            prompt += "\n\n" + agent["extra_prompt"]
        for backend in ("claude", "codex"):
            out_path = os.path.join(review_dir, f"{agent['name']}-{backend}.md")
            out_file = open(out_path, "w")
            proc = subprocess.Popen(
                [sys.executable, this_script, backend, "-p", prompt],
                stdout=out_file,
                stderr=devnull,
            )
            procs.append((proc, out_file, out_path, agent))

    for proc, out_file, _, _ in procs:
        proc.wait()
        out_file.close()

    succeeded = []
    for proc, _, out_path, agent in procs:
        if proc.returncode == 0 and os.path.getsize(out_path) > 0:
            succeeded.append((out_path, agent))

    devnull.close()

    if not succeeded:
        print("error: all agents failed", file=sys.stderr)
        sys.exit(1)

    # Build merger prompt with agent metadata annotations.
    file_list_parts = []
    for out_path, agent in succeeded:
        meta = []
        if agent.get("role"):
            meta.append(f"role: {agent['role']}")
        if agent.get("extra_prompt"):
            meta.append(f"extra_prompt: {agent['extra_prompt']}")
        annotation = f" ({'; '.join(meta)})" if meta else ""
        file_list_parts.append(f"- {out_path}{annotation}\n")
    file_list = "".join(file_list_parts)
    parts = [
        context,
        "The following code review files are available (read each one):\n" + file_list,
    ]
    if notes_content:
        parts.append(NOTES_PROMPT + notes_content)
    parts.append(DEFAULT_MERGER_PROMPT)
    parts.append(REVIEW_CONTENT_PROMPT)
    parts.append(REVIEW_OUTPUT_FORMAT)
    merger_prompt = "\n".join(parts)
    raw_path = os.path.join(review_dir, "final-review.raw")
    with open(raw_path, "w") as raw_file:
        merger_proc = subprocess.run(
            [sys.executable, this_script, "claude", "-p", merger_prompt],
            stdout=raw_file,
            stderr=subprocess.DEVNULL,
        )

    if merger_proc.returncode != 0 or os.path.getsize(raw_path) == 0:
        print("error: merger failed", file=sys.stderr)
        sys.exit(1)

    with open(raw_path) as f:
        elements = parse_review(f.read())

    if not elements:
        print("error: no items parsed from merger output", file=sys.stderr)
        sys.exit(1)

    # Inject standing questions as final review items.
    elements.append({
        "body": "Automatically amend commits after making the requested changes?",
        "options": [
            {"text": "yes, amend"},
            {"text": "no, I will amend manually"},
        ],
    })
    elements.append({
        "body": "Rebase onto origin/main?",
        "options": [
            {"text": "yes, rebase"},
            {"text": "no"},
        ],
    })

    final_path = os.path.join(review_dir, "final-review.md")
    with open(final_path, "w") as f:
        f.write(render_markdown(elements))

    json_path = os.path.join(review_dir, "final-review.json")
    with open(json_path, "w") as f:
        json.dump(render_json(elements), f, indent=2)
        f.write("\n")

    print(final_path)


def main():
    p = argparse.ArgumentParser(description="Unified codereview tooling")
    sub = p.add_subparsers(dest="command", required=True)

    sub.add_parser("read-notes", help="Read relevant codereview notes")

    w = sub.add_parser(
        "write-notes",
        help="Write a codereview note on HEAD (also cleans up old notes)",
    )
    w.add_argument("-m", "--message", help="Note content (reads stdin if omitted)")
    w.add_argument("-F", "--file", help="Read note content from file")

    sub.add_parser("context", help="Print git context for the current branch")

    c = sub.add_parser("claude", help="Run a claude CLI review")
    c.add_argument("-p", "--prompt", required=True, help="Prompt to pass to claude")

    x = sub.add_parser("codex", help="Run a codex CLI review")
    x.add_argument("-p", "--prompt", required=True, help="Prompt to pass to codex")

    r = sub.add_parser("run", help="Run paired claude+codex review agents with merger")
    r.add_argument("review_dir", help="Path to the review directory")

    args = p.parse_args()
    {
        "read-notes": cmd_read_notes,
        "write-notes": cmd_write_notes,
        "context": cmd_context,
        "claude": cmd_claude,
        "codex": cmd_codex,
        "run": cmd_run,
    }[args.command](args)


if __name__ == "__main__":
    main()
