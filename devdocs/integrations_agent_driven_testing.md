# Testing GitHub Integration (Agent-Driven, Real GitHub)

This documents how to test the full `integrations setup github` flow end-to-end
against real GitHub, running on an exe.dev VM.

## Prerequisites

- An exe.dev VM (this doc assumes paths like `/home/exedev/exe`)
- A GitHub test account (we use `sketchdevtestuser`)
- A GitHub App configured for development (we use `exe-dev-dev`)
- Environment variables in `~/.envrc-github`:
  - `EXE_GITHUB_APP_CLIENT_ID`
  - `EXE_GITHUB_APP_CLIENT_SECRET`
  - `EXE_GITHUB_APP_SLUG`
  - `EXE_GITHUB_APP_ID`
  - `EXE_GITHUB_APP_PRIVATE_KEY` (RSA PEM)
- TOTP key for the test account (if 2FA is enabled)
- `pyotp` installed (`pip3 install pyotp`) for generating TOTP codes

## Build

```bash
cd /home/exedev/exe  # or your worktree
source ~/.envrc-github
go build -o /tmp/exed-ghtest ./cmd/exed/
```

Build sshpiper if not already built:

```bash
cd deps/sshpiper
GOTOOLCHAIN=go1.26.1 go build -o sshpiperd ./cmd/sshpiperd
```

## Start exed

```bash
# Clean DB for a fresh start
rm -rf /tmp/ghtest-db

# Start in a tmux session
tmux new-session -d -s ghtest
tmux send-keys -t ghtest 'source ~/.envrc-github && /tmp/exed-ghtest -stage=local -start-exelet -db /tmp/ghtest-db' Enter
```

Wait for `server started` in the logs. Exed listens on:
- HTTP: `:8080`
- SSH: `:2223` (direct)
- Piper plugin: `:2224`

## Start sshpiper

On an exe.dev box, `AutoStartSSHPiper` is false, so start it manually:

```bash
HOST_KEY=$(sqlite3 /tmp/ghtest-db "SELECT private_key FROM ssh_host_key WHERE id = 1;")
HOST_KEY_B64=$(printf '%s' "$HOST_KEY" | base64 -w 0)

deps/sshpiper/sshpiperd \
  --log-level=DEBUG \
  --drop-hostkeys-message \
  --port=2222 \
  --address=0.0.0.0 \
  --server-key-data="$HOST_KEY_B64" \
  grpc --endpoint=localhost:2224 --insecure
```

Alternatively, use `./sshpiper.sh 2224` (but it reads from `exe.db` by default,
not `/tmp/ghtest-db`).

## Register a test user via SSH

```bash
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 localhost
```

1. Enter an email (e.g., `test@example.com`)
2. The `[DEV-ONLY] Emailed link` will appear with a token URL
3. In another terminal, POST the verification:
   ```bash
   curl -k -X POST http://localhost:8080/verify-email \
     -d 'token=<TOKEN>&source=exemenu'
   ```
4. The SSH session completes registration

## Run the GitHub integration flow

In the SSH session:
```
integrations setup github
```

This prints an authorization URL like:
```
https://phil-exe-dev.exe.xyz:8080/r/<state>
```

### Browser side

1. Navigate to the URL (use `http://localhost:8080/r/<state>` if testing locally)
2. GitHub login page appears — sign in as the test account
3. GitHub shows the OAuth authorization page — click "Authorize"
4. If the app isn't installed yet, GitHub shows an "Install & Authorize" page — click it
5. GitHub redirects to `/github/callback` — the browser shows "GitHub Connected"

### SSH side

The SSH session prints:
```
Connected: sketchdevtestuser (sketchdevtestuser)
Manage app permissions: https://github.com/apps/exe-dev-dev
View connections: integrations setup github --list
```

## Verify the connection

```
integrations setup github --verify
```

This calls the GitHub API with the stored token to confirm it's valid.

## Useful commands

```
integrations setup github          # connect a GitHub account
integrations setup github --list    # list connected accounts
integrations setup github --verify  # verify tokens work
integrations setup github -d        # disconnect all accounts
```

## Inspecting state

```bash
sqlite3 /tmp/ghtest-db "SELECT user_id, github_login, installation_id, target_login FROM github_accounts;"
```

## GitHub device verification

GitHub may prompt for "device verification" (separate from 2FA) when logging
in from a new browser/location. This sends an email code to the account's
primary email. You need access to that email to proceed. This is a one-time
thing per browser session — once verified, subsequent logins skip it.

## Re-authorization after deleting DB rows

If you delete rows from `github_accounts` but the user has already authorized
the app on GitHub, re-running `integrations setup github` will:
1. OAuth authorize (GitHub skips the consent screen since already authorized)
2. Discover existing installations via `GET /user/installations`
3. Re-store the account rows

This tests the idempotent upsert path.

## Gotchas

- The exe.dev HTTPS proxy makes `https://phil-exe-dev.exe.xyz:8080/` work
  externally, but the browser tool may need `http://localhost:8080/` instead.
- `sshpiper.sh` reads host keys from `exe.db`, not from a custom DB path.
  Extract the key manually for custom DB locations.
- GitHub user access tokens from the OAuth flow expire. The `--verify` command
  automatically refreshes expired tokens using the stored `refresh_token`.
- Do NOT set `TEST_GITHUB_TOKEN_URL` or `TEST_GITHUB_API_URL` — those override
  endpoints to point at a mock server.
