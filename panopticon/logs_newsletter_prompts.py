"""Prompts for the logs analyzer agent.

Edit these strings to change agent behavior without touching pipeline code.
"""

# -- Field descriptions (shown to the agent as input/output schema) ----------

CLICKHOUSE_DESC = "ClickHouse analytics database"
GITHUB_DESC = "GitHub repository"

CODE_DESC = "All tracked files in the git repo as a {path: content} dict."

COMMITS_DESC = (
    "Git commit history (newest first) as a list of dicts with keys: "
    "sha, short, subject, author, date (ISO 8601), body. "
    "For diffs, call commit_diff(sha)."
)

# -- Source descriptions for the instruction ---------------------------------

SOURCE_DESCS = {
    "clickhouse": "a ClickHouse analytics database",
    "github": "the GitHub repository (issues, PRs, and comments)",
    "worktree": "the local git worktree (source code and commit history)",
}

# -- Main signature instruction ----------------------------------------------
# {date_str}, {time_str}, and {sources_desc} are filled at runtime.

SIGNATURE_INSTRUCTION = """\
Write a daily logs brief for the team.

Today is {date_str} ({time_str}). Your sources are {sources_desc}.

Your audience is the engineering and product team.
The value of this brief is that it keeps them in touch with operational details that they might not otherwise notice.
Except when debugging, they typically don't look at logs, so they generally won't notice anomalies, patterns, noisy log lines, etc.
There's always a lot to learn from being close to the data; be their eyes and ears.

You have access to the git worktree and GitHub to inform your analysis.
The engineering team has other mechanisms to stay on top of those data sources.
They are here to give your broader visibility for better understanding, but your analysis should be logs-centered.

Keep it to a few tight bullets with links. Top priorities only.
Trust readers to follow threads when something catches their eye.

Review a broader arc of logs to understand the baseline;
readers read this newsletter every day.
Then focus on the last 24 hours and report what's new against that backdrop.
Readers will build cumulative understanding over time.

Format in Slack mrkdwn. No title, date header, preamble, or sign-off.\
"""
