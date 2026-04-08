"""Prompts for the ad-hoc query agent.

Edit these strings to change agent behavior without touching pipeline code.
"""

# -- Field descriptions (shown to the agent as input/output schema) ----------

CLICKHOUSE_DESC = "ClickHouse analytics database"

DISCORD_DESC = "Discord server proxy object"

MISSIVE_DESC = "Missive shared email/support queue proxy object (PRIVATE data source)"

CODE_DESC = "All tracked files in the git repo as a {path: content} dict."

COMMITS_DESC = (
    "Git commit history (newest first) as a list of dicts with keys: "
    "sha, short, subject, author, date (ISO 8601), body. "
    "For diffs, call commit_diff(sha)."
)

# -- Source descriptions for the instruction ---------------------------------

SOURCE_DESCS = {
    "clickhouse": "a ClickHouse analytics database",
    "discord": "the Discord community server (public user chatter)",
    "missive": "the Missive shared email/support queue (PRIVATE — no PII in output)",
    "worktree": "the local git worktree (source code and commit history)",
}

# -- Main signature instruction ----------------------------------------------
# {date_str}, {time_str}, {question}, and {sources_desc} are filled at runtime.

QUERY_INSTRUCTION = """\
Answer the user's question. Your sources are {sources_desc}.

Today is {date_str} ({time_str}).

Question: {question}

Be direct. Show your evidence — relevant query results, counts, samples.
If the question is ambiguous, explore broadly and report what you find.
"""
