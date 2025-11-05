# e3e security probe

This package contains the cron-friendly security probe that exercises exe.dev
boxes with Codex and Claude. The test flow is:

1. `ssh exe.dev new --json` to create a fresh box.
2. Connect to the new box over SSH.
3. Run the prompt from `security_probe_prompt.txt` through Codex and Claude.
4. Verify that both agents finish with `ALL CLEAR`.
5. Delete the box, even on failure.

If either agent reports anything other than `ALL CLEAR`, the test fails (and CI
triggers a Discord alert). When a failure occurs, the full Codex/Claude report
is printed in the test output so responders can see the details without
downloading artifacts.

## Running locally

The test is skipped unless `EXE_E3E_ENABLE` is set. Set the required
environment and run `go test`:

```bash
export EXE_E3E_ENABLE=1
export EXE_E3E_OPENAI_API_KEY=...   # OpenAI / Codex key
export EXE_E3E_ANTHROPIC_API_KEY=...# Claude key
export EXE_E3E_SSH_KEY_PATH=...     # SSH key that can create exe.dev boxes
# optional:
# export EXE_E3E_SSH_USER=...
# export EXE_E3E_SSH_HOST=exe.dev
# export EXE_E3E_SSH_KNOWN_HOSTS=...

go test -count=1 ./e3e
```

The prompt text lives in `security_probe_prompt.txt`; tweak it there if needed.

## CI secrets

The GitHub Actions workflow expects the following secrets:

- `E3E_SSH_PRIVATE_KEY` – private key with access to `ssh exe.dev`
- `E3E_SSH_USER` – username that owns the key
- `E3E_OPENAI_API_KEY` – Codex/OpenAI API key
- `E3E_ANTHROPIC_API_KEY` – Claude API key

The workflow writes the SSH key to `~/.ssh/exe-e3e`, sets the matching env vars,
runs `go test ./e3e`, and pings Discord with a link to the failing run if the
probe reports anything other than `ALL CLEAR`.
