# e3e security probe

This command contains the cron-friendly security probe that exercises exe.dev
boxes with Codex and Claude. The probe flow is:

1. `ssh exe.dev new --json` to create a fresh box.
2. Connect to the new box over SSH.
3. Run the prompt from `security_probe_prompt.txt` through Codex and Claude.
4. Verify that both agents finish with `ALL CLEAR`.
5. Delete the box, even on failure.

The agents use the in-VM LLM gateway (`http://169.254.169.254/gateway/llm/...`)
for API access, so no external API keys are needed. The gateway charges against
the exe.dev account that owns the SSH key used to create the box (by default,
the `e3e` user).

If either agent reports anything other than `ALL CLEAR`, the probe fails (and CI
triggers a Discord alert). When a failure occurs, the full Codex/Claude report
is printed in the test output so responders can see the details without
downloading artifacts.

## Running locally

Set the required environment and run the command:

```bash
export EXE_E3E_SSH_KEY_PATH=...     # SSH key that can create exe.dev boxes
# optional:
# export EXE_E3E_SSH_USER=...
# export EXE_E3E_SSH_HOST=exe.dev
# export EXE_E3E_SSH_KNOWN_HOSTS=...

go run ./cmd/e3e
```

The prompt text lives in `security_probe_prompt.txt`; tweak it there if needed.
The probe enforces a 1h timeout, matching the CI configuration.

## CI secrets

The GitHub Actions workflow expects the following secrets:

- `E3E_SSH_PRIVATE_KEY` – private key with access to `ssh exe.dev`
- `E3E_SSH_USER` – username that owns the key

The workflow writes the SSH key to `~/.ssh/exe-e3e`, sets the matching env vars,
runs `go run ./cmd/e3e`, and reports the outcome to Slack—posting the failure
summary in `#oops` and refreshing the `#btdb` ledger entry so we can see when
the bot last ran.
