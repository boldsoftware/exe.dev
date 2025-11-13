# e4e documentation sweep

The `e4e` command runs the automated documentation review. When the probe
executes, it launches a single Codex agent via `codex exec` (using
`gpt-5-codex-mini` with the `prompt.md` instructions) and gives the agent
read-only access to the repo. The agent is expected to read everything in
`docs/`, cross-check behavior against the rest of the tree, and finish with a
`# DOCUMENTATION REPORT` section. If the report ends in `OK` the run succeeds;
otherwise the command exits non-zero with the reported findings.

## Running locally

Running locally requires the Codex CLI plus a Codex/OpenAI API key.

```bash
export EXE_E4E_OPENAI_API_KEY=...   # Codex/OpenAI key
go run ./cmd/e4e
```

`EXE_E4E_OPENAI_API_KEY` is the only required input. The agent always scans the
checked-in `docs/` directory and writes its reasoning and final report to stdout;
there is no JSON artifact to inspect.

## CI inputs

`exe-e4e-docs.yml` expects these secrets:

- `E4E_OPENAI_API_KEY` – Codex API key used by the agent
- `EXE_SLACK_BOT_TOKEN` – token used by Slack posting scripts

The workflow captures the `go run ./cmd/e4e` output to a temp file, posts the
failure log to `#oops` when the sweep finds issues, and refreshes
the `#btdb` ledger entry for the `e4e-docs` bot on every run so we can see the
latest status.
