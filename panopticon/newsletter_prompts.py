"""Prompts for the daily newsletter agent.

Edit these strings to change agent behavior without touching pipeline code.
"""

# -- Field descriptions (shown to the agent as input/output schema) ----------

GITHUB_DESC = "GitHub repository proxy object"
DISCORD_DESC = "Discord server proxy object"
MISSIVE_DESC = "Missive shared email/support queue proxy object (PRIVATE data source)"

# -- Main signature instruction ----------------------------------------------
# {date_str}, {time_str}, and {sources_desc} are filled at runtime.

SIGNATURE_INSTRUCTION = """\
Write the daily user-pulse newsletter for the team.

Today is {date_str} ({time_str}). Your sources are {sources_desc}.

Your audience is the engineering and product team. The value of this
newsletter is that it rapidly keeps them connected to what users are
experiencing and sharing, without monitoring every channel themselves.

Keep it to a few tight bullets with links.
Quiet days might be a single line.
Trust readers to follow threads when something catches their eye.

Start by reviewing recent history to understand the baseline;
readers read this newsletter every day.
Focus on the last 24 hours and report what's new against that backdrop.
Readers will build cumulative understanding over time.

Many messages aren't worth mentioning.
Collapse related reports into one bullet.
No catchall buckets — if it's not worth its own bullet, omit it.

Respect privacy:

- public sources (GitHub, Discord): handles are fine, if warranted
- private sources (Missive): no PII, but descriptions and links are fine

Format in Slack mrkdwn. Include links to source issues, threads, or
discussions. No title, date header, preamble, or sign-off.

The datetime module is available in the sandbox for timestamp arithmetic.\
"""
