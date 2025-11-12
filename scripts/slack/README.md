## Slack helpers

Each helper can be executed with `uv run <script> ...` from the repo root:

- `uv run scripts/slack/post_message.py --channel oops --message "text"`
  Posts an arbitrary message to a Slack channel using `EXE_SLACK_BOT_TOKEN`.

- `uv run scripts/slack/update_bot_status.py BOT_NAME --status success --run-url RUN_URL`
  Upserts the JSON entry inside `#btdb` that tracks when a bot last ran. The status string is free-form (common values: `success`, `failure`, `cancelled`, `running`, `unknown`). Messages are rendered inside triple backticks so they look good in Slack while remaining machine-parsable.

All scripts rely on `EXE_SLACK_BOT_TOKEN` being present in the environment when posting or updating messages.
