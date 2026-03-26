# Testing Integrations

How to test the integrations feature (HTTP proxy and GitHub types) end-to-end.

## Architecture Overview

Integrations attach external services to VMs. Data flow:

1. **exed** (execore) -- manages integration CRUD, OAuth flows, stores config in SQLite
2. **exelet** (metadata service) -- runs on container hosts, proxies requests from VMs to external targets
3. **VM** -- accesses integrations via `<name>.int.exe.cloud` (resolved to 169.254.169.254, proxied by exelet)

Key source files:

| File | Purpose |
|------|---------|
| `execore/ssh_integrations_command.go` | CLI command handler |
| `execore/ssh_integrations_github.go` | GitHub OAuth/setup flow |
| `execore/github_callback.go` | GitHub OAuth callback handler |
| `execore/integration_proxy.go` | Server-side config endpoint (exelet fetches from this) |
| `execore/integration_validate.go` | Name, header, and URL validation |
| `exelet/metadata/metadata.go` | Exelet-side proxy (`handleIntegrationProxy`) |

## Unit Tests

```bash
go test -count=1 ./execore/ -run "TestValidateIntegrationName|TestValidateHTTPHeader|TestValidateTargetURL"
```

## End-to-End Tests (e1e)

Require a local exed+exelet stack with a real container host.

```bash
# All integration e1e tests
go test -count=1 ./e1e -run "^TestIntegration"

# Specific tests
go test -count=1 ./e1e -run TestIntegrationsCommand                    # CRUD, validation, golden file
go test -count=1 ./e1e -run TestIntegrationsBearerFlag                 # --bearer shorthand
go test -count=1 ./e1e -run TestIntegrationsAttachDetach               # attach/detach lifecycle
go test -count=1 ./e1e -run TestIntegrationsRename                     # rename flow
go test -count=1 ./e1e -run TestIntegrationsProxy                      # proxy forwarding from inside VM
go test -count=1 ./e1e -run TestIntegrationsSetupGitHub$               # GitHub OAuth (has installations)
go test -count=1 ./e1e -run TestIntegrationsSetupGitHubNoInstallations # new user, no installations, polling
go test -count=1 ./e1e -run TestIntegrationsSetupGitHubOrg             # personal + org installations
go test -count=1 ./e1e -run TestIntegrationsGitHubOrphanInstallCallback # install callback with no pending setup
go test -count=1 ./e1e -run TestIntegrationsGitHubOrphanOAuthCallback  # OAuth callback with bad state
go test -count=1 ./e1e -run TestIntegrationsSetupGitHubWrongAccount    # wrong account retry flow
go test -count=1 ./e1e -run TestIntegrationsAddGitHub                  # GitHub integration add
go test -count=1 ./e1e -run TestIntegrationAttachmentSpecs             # attachment spec parsing
```

Golden files: `e1e/golden/TestIntegration*.txt`. Update with `-update`.

## Manual Testing Against Local exed (with Real GitHub)

Tests the full OAuth flow against real GitHub using the dev GitHub App. Run from an exe.dev VM.

### Prerequisites

- **GitHub dev app credentials and test user passwords** in `~/.envrc-github` (env vars `EXE_GITHUB_APP_*`, `SKETCHDEVTESTUSER_PASSWORD`, `SKETCHDEVTESTUSER2_PASSWORD`). This file lives on exe.dev VMs outside the repo -- never commit it.
- **GitHub test accounts**:
  - `sketchdevtestuser` -- personal account, member of `sketchdevtestuserorg`. Email: `github@phil-exe-dev.exe.xyz` (delivered to `~/Maildir/new/` on `phil-exe-dev` VM).
  - `sketchdevtestuser2` -- second account for multi-account testing. Email: `github2@phil-exe-dev.exe.xyz`.
- A browser logged into the test GitHub account

### Steps

1. Build and start local exed with GitHub env vars:

```bash
make exelet
go build -o /tmp/exed-local ./cmd/exed/
tmux new-session -d -s exed 'bash -c "source ~/.envrc-github && /tmp/exed-local -stage=local -start-exelet -db tmp"'
sleep 5
tmux capture-pane -t exed -p | tail -5  # verify "server started"
```

2. SSH in and register (fresh DB each time):

```bash
tmux new-session -d -s sshtest 'ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2223 localhost'
sleep 3
# Enter email when prompted, then verify via the DEV-ONLY link:
# curl -s -X POST "http://localhost:8080/verify-email" -d "token=<TOKEN>&source=exemenu"
```

3. Run GitHub setup:

```bash
tmux send-keys -t sshtest "integrations setup github" Enter
```

The SSH session prints a URL like `https://phil-exe-dev.exe.xyz:8080/r/<state>`.

4. Resolve the redirect (the exe.dev HTTPS proxy may not reach local port 8080):

```bash
curl -s -o /dev/null -w "%{redirect_url}" "http://localhost:8080/r/<state>"
# Returns: https://github.com/login/oauth/authorize?client_id=...&state=...
```

5. Visit the GitHub OAuth URL in the browser. GitHub will either show "Install & Authorize" (first time) or auto-authorize and redirect.

6. The SSH session should unblock and show `Connected: sketchdevtestuser`.

### What to Test

```bash
integrations setup github --list     # list connected accounts
integrations setup github --verify   # verify tokens work
integrations add github --name=ghtest --repository=sketchdevtestuser/test-repo
integrations list
integrations remove ghtest
integrations setup github -d         # disconnect
integrations setup github            # reconnect (handles both fresh + reconnection)
```

### Testing with Multiple GitHub Accounts (Wrong Account Retry)

**Always manually test this when changing the OAuth flow.** The e1e mock cannot reproduce all of GitHub's account-switcher behaviors.

Prerequisites: two GitHub accounts in the same browser session (use GitHub's "Add another account" at `https://github.com/login?add_account=1`). Test accounts: `sketchdevtestuser` and `sketchdevtestuser2`.

1. Run `integrations setup github`
2. Open the authorize URL -- GitHub shows "Select user to authorize"
3. **Wrong account**: pick the account without the app installed. The SSH session detects the auth failure and offers retry with a new URL (up to 3 attempts).
4. **Correct account**: pick the account with the app installed. Should connect successfully.
5. Verify with `--list` and `--verify`, clean up with `-d`

After testing, uninstall the dev app from test accounts at https://github.com/settings/installations.

### Testing with Organizations

1. **Org already has app**: after OAuth, both personal and org installations are discovered. `--list` shows both.
2. **Installing on new org**: if no installations found after OAuth, browser redirects to GitHub App install page. Choose org and install. SSH session polls and detects automatically.
3. **Orphan install callback**: installing on an org after SSH session finishes shows a friendly "INSTALLED" page. Run `integrations setup github` again to sync.
4. **Org repo integrations**: `integrations add github --name=test --repository=orgname/repo` works if org has app installed.

### Cleanup

```bash
tmux kill-session -t sshtest
tmux kill-session -t exed
```

## Manual Testing Against Production

```bash
# SSH in (use invite code if needed for fresh account)
ssh <invite-code>@exe.dev

# Run GitHub setup
integrations setup github

# Resolve redirect
curl -s -o /dev/null -w "%{redirect_url}" "https://exe.dev/r/<state>"
# Visit the resulting GitHub OAuth URL

# Verify
integrations setup github --verify
integrations add github --name=ghtest --repository=sketchdevtestuser/test-repo
integrations list
integrations remove ghtest
integrations setup github -d
```

## Testing HTTP Proxy Integrations

```bash
# From your exe.dev SSH session
integrations add http-proxy --name=myproxy --target=https://httpbin.org --header=X-Test:hello
integrations attach myproxy vm:<vm-name>

# From inside the VM
curl http://myproxy.int.exe.cloud/get           # X-Test header injected
curl -X POST -d '{"a":1}' http://myproxy.int.exe.cloud/post
curl https://myproxy.int.exe.cloud/get           # HTTPS also works
```

Verify:
- Header injection appears in upstream requests
- POST body forwarding works
- Cross-VM isolation (unattached VM cannot access the integration)
- Private IP blocking (targets resolving to private IPs rejected at dial time)
- Detach propagation (after `integrations detach`, VM loses access; ~1 min cache TTL via `IntegrationCacheTTL` in `exelet/metadata/metadata.go`)

## Troubleshooting

**"Authorization failed -- wrong GitHub account"**: User has multiple GitHub accounts and picks the wrong one. OAuth succeeds but the token gets 401 on `GET /user`. SSH session detects this and offers retry (up to 3 attempts) with a new URL.

**"GitHub user lookup failed" / 401 Bad credentials**: Retry exhausted or non-auth failure. Possible causes: transient GitHub API issue, OAuth code reuse, expired/revoked token.

**Browser stuck after Install/Uninstall**: GitHub redirects to the app's callback URL (`http://localhost:8080` for dev). If browser can't reach localhost, navigate away manually -- server-side state may already be handled.

**"unknown or expired setup" after org install**: Install callback arrived after SSH session completed. Shows "INSTALLED" page instead of error. Run `integrations setup github` again to sync.
