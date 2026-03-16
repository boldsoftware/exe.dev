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

## Manual / Staging Testing

For testing OAuth flows, GitHub integration, and the proxy from inside a real VM,
use a staging account on `exe-staging.dev`.

### What You Need

- **Staging account** — sign up on exe-staging.dev; needs email verification
  (use an address that receives mail on this VM via `~/Maildir/new/`)
- **Billing** — staging uses Stripe test mode; use card `4242 4242 4242 4242`
  with any future expiry and any CVC
- **GitHub test user** — a GitHub account for OAuth testing; credentials are in
  the project's shared secrets (not inlined here)
- **GitHub App** — must be installed for the test user on the staging GitHub App;
  the app config is set via `EXE_GITHUB_APP_*` env vars on the staging exed

### GitHub App Environment Variables

The GitHub integration requires these env vars on exed:

- `EXE_GITHUB_APP_CLIENT_ID` — OAuth client ID
- `EXE_GITHUB_APP_CLIENT_SECRET` — OAuth client secret
- `EXE_GITHUB_APP_SLUG` — app slug (for install URLs)
- `EXE_GITHUB_APP_ID` — numeric app ID (for installation token minting)
- `EXE_GITHUB_APP_PRIVATE_KEY` — PEM private key (for signing JWTs)

Without all of these, `integrations setup github` will report that GitHub
integration is disabled.

### Testing HTTP Proxy Integrations

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

### Testing GitHub Integrations

```bash
# From your exe.dev SSH session:
integrations setup github
# Follow the OAuth flow in the browser

integrations setup github --list    # verify connected account
integrations setup github --verify  # check token is still valid

integrations add github --name=mygh --repository=owner/repo
integrations attach mygh vm:<vm-name>

# Then SSH into the VM and test:
git clone http://mygh.int.exe.cloud/owner/repo.git
git clone https://mygh.int.exe.cloud/owner/repo.git
```

Things to verify:
- OAuth flow completes (check the "Connected" page in browser)
- OAuth denial is handled (click "Cancel" on GitHub — should show error, not hang)
- Clone works over both HTTP and HTTPS
- Only `.git` paths are proxied (non-git paths should be rejected)
- Only configured repositories are accessible
- `--verify` detects expired/revoked tokens

### Cleanup

Remember to clean up staging resources after testing:

```bash
integrations detach <name> vm:<vm>
integrations remove <name>
# Delete test VMs
```
