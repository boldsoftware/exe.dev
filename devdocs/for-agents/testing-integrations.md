# Testing Integrations

How to test the integrations feature (HTTP proxy and GitHub types) end-to-end.

## Architecture Overview

Integrations let users attach external services to their VMs. The data flow is:

1. **exed** (execore) — manages integration CRUD, OAuth flows, stores config in SQLite
2. **exelet** (metadata service) — runs on container hosts, proxies requests from VMs to external targets
3. **VM** — accesses integrations via `<name>.int.exe.cloud` (resolved to 169.254.169.254, proxied by exelet)

Key source files:
- `execore/ssh_integrations_command.go` — CLI command handler
- `execore/ssh_integrations_github.go` — GitHub OAuth/setup flow
- `execore/github_callback.go` — GitHub OAuth callback handler
- `execore/integration_proxy.go` — server-side config endpoint (exelet fetches from this)
- `execore/integration_validate.go` — name, header, and URL validation
- `exelet/metadata/metadata.go` — exelet-side proxy (`handleIntegrationProxy`)

## Unit Tests

```bash
# Validation tests (fast, no server needed)
go test -count=1 ./execore/ -run "TestValidateIntegrationName|TestValidateHTTPHeader|TestValidateTargetURL"
```

## End-to-End Tests (e1e)

The e1e tests require a local exed+exelet stack with a real container host.

```bash
# Run all integration e1e tests
go test -count=1 ./e1e -run "^TestIntegration"

# Specific tests
go test -count=1 ./e1e -run TestIntegrationsCommand          # CRUD, validation, golden file
go test -count=1 ./e1e -run TestIntegrationsBearerFlag       # --bearer shorthand
go test -count=1 ./e1e -run TestIntegrationsAttachDetach     # attach/detach lifecycle
go test -count=1 ./e1e -run TestIntegrationsRename           # rename flow
go test -count=1 ./e1e -run TestIntegrationsProxy            # proxy forwarding from inside a VM
go test -count=1 ./e1e -run TestIntegrationsSetupGitHub      # GitHub OAuth flow
go test -count=1 ./e1e -run TestIntegrationsAddGitHub        # GitHub integration add
go test -count=1 ./e1e -run TestIntegrationAttachmentSpecs   # attachment spec parsing
```

Golden files live in `e1e/golden/TestIntegration*.txt`. Update them with `-update`.

## Manual Testing Against Local exed (with Real GitHub)

This tests the full OAuth flow against real GitHub using the dev GitHub App.
Run this from an exe.dev VM.

### Prerequisites

- **GitHub dev app credentials** in `~/.envrc-github` (env vars `EXE_GITHUB_APP_*`)
- **GitHub test account**: user `sketchdevtestuser` (password and credentials
  in the team's shared secrets)
- A browser logged into the test GitHub account

### Steps

1. Build and start local exed with GitHub env vars:

```bash
# Build exelet first to avoid OOM on small VMs
make exelet
go build -o /tmp/exed-local ./cmd/exed/

# Start in tmux with GitHub env vars
tmux new-session -d -s exed 'bash -c "source ~/.envrc-github && /tmp/exed-local -stage=local -start-exelet -db tmp"'
sleep 5
tmux capture-pane -t exed -p | tail -5  # verify "server started" message
```

2. SSH in and register (fresh DB each time):

```bash
tmux new-session -d -s sshtest 'ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2223 localhost'
sleep 3
# Enter email when prompted, then verify via the DEV-ONLY link:
# curl -s -X POST "http://localhost:8080/verify-email" -d "token=<TOKEN>&source=exemenu"
```

3. Run the GitHub setup:

```bash
tmux send-keys -t sshtest "integrations setup github" Enter
```

The SSH session prints a URL like:
```
Authorize your GitHub account:
  https://phil-exe-dev.exe.xyz:8080/r/<state>

Waiting...
```

4. Resolve the redirect (the exe.dev HTTPS proxy may not reach local port 8080,
   so get the target URL and visit it directly):

```bash
curl -s -o /dev/null -w "%{redirect_url}" "http://localhost:8080/r/<state>"
# Returns: https://github.com/login/oauth/authorize?client_id=...&state=...
```

5. Visit that GitHub OAuth URL in the browser (already logged in as
   `sketchdevtestuser`). GitHub will either:
   - Show an "Install & Authorize" page (first time) — click Install & Authorize
   - Auto-authorize and redirect (if the app is already installed)

   GitHub redirects to `http://localhost:8080/github/callback?code=...&state=...`
   which the local exed handles.

6. The SSH session should unblock and show:
```
Connected: sketchdevtestuser
```

### What to Test

After setup, run through the full command set:

```bash
# Verify the connection
integrations setup github --list
# Expected: "sketchdevtestuser (installed on sketchdevtestuser)"

integrations setup github --verify
# Expected: "✓ sketchdevtestuser ... — verified (API user: sketchdevtestuser)"

# Add a GitHub integration
integrations add github --name=ghtest --repository=sketchdevtestuser/test-repo
# Expected: "Added integration ghtest"

# List integrations
integrations list
# Expected: "ghtest  github  repos=sketchdevtestuser/test-repo  (none)"

# Remove integration
integrations remove ghtest
# Expected: "Removed integration ghtest"

# Disconnect
integrations setup github -d
# Expected: "Disconnected GitHub: sketchdevtestuser (sketchdevtestuser)"

# Reconnect (run setup again after disconnect)
integrations setup github
# Follow the OAuth flow again — should reconnect
```

### Cleanup

Kill tmux sessions when done:

```bash
tmux kill-session -t sshtest
tmux kill-session -t exed
```

Optionally uninstall the dev GitHub App from the test account at
https://github.com/settings/installations (look for "exe.dev dev").

## Manual Testing Against Production

Test the real prod flow with the same GitHub test account.

### Steps

1. SSH into prod exe.dev (use an invite code if needed for a fresh account):

```bash
ssh <invite-code>@exe.dev
# Register with an email that delivers to this VM, e.g.:
# phil-test@<vm-name>.exe.xyz
# Check ~/Maildir/new/ for the verification email
```

2. Run the GitHub setup:

```bash
integrations setup github
```

3. Resolve and visit the redirect URL:

```bash
curl -s -o /dev/null -w "%{redirect_url}" "https://exe.dev/r/<state>"
# Visit the resulting GitHub OAuth URL in the browser
```

4. Verify the same commands work as in local testing:

```bash
integrations setup github --verify
integrations add github --name=ghtest --repository=sketchdevtestuser/test-repo
integrations list
integrations remove ghtest
integrations setup github -d
```

### Cleanup

Disconnect the GitHub account and remove any test integrations before exiting.

## Testing HTTP Proxy Integrations

```bash
# From your exe.dev SSH session:
integrations add http-proxy --name=myproxy --target=https://httpbin.org --header=X-Test:hello
integrations attach myproxy vm:<vm-name>

# Then SSH into the VM and test:
curl http://myproxy.int.exe.cloud/get          # should see X-Test header
curl -X POST -d '{"a":1}' http://myproxy.int.exe.cloud/post
curl https://myproxy.int.exe.cloud/get          # HTTPS also works
```

Things to verify:
- Header injection (the configured header appears in upstream requests)
- POST body forwarding
- Cross-VM isolation (VM without the attachment can't access the integration)
- Private IP blocking (target URLs resolving to private IPs are rejected at dial time)
- Detach propagation (after `integrations detach`, the VM loses access; note the
  ~1 minute cache TTL in exelet — `IntegrationCacheTTL` in `exelet/metadata/metadata.go`)

## Troubleshooting

**"GitHub user lookup failed" / 401 Bad credentials**: This occurs when the
server calls `GET https://api.github.com/user` with the access token from the
OAuth code exchange. Possible causes:
- Transient GitHub API issue (token propagation delay)
- OAuth code reuse (browser retry hitting the callback twice)
- Expired/revoked token (if testing with old credentials)

If it doesn't reproduce, it's likely a transient GitHub-side issue.

**Browser stuck after clicking Install/Uninstall**: GitHub redirects to the
callback URL configured on the app. For the dev app this is
`http://localhost:8080`. If the browser can't reach localhost:8080 (e.g.,
you're using a remote browser), the page will hang. Navigate away manually;
the server-side state may have already been handled.

**"flag provided but not defined: -reconnect"**: The `--reconnect` flag was
removed. Just run `integrations setup github` again; it handles both fresh
setup and reconnection.
