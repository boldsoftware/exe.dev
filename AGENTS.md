- exe.dev is a service users can use to start VMs with persistent disks. Read README.md and devdocs/architecture.md for more. The ./cmd/exed binary implements the web and SSH frontend and acts as VM controller, instructing ctr-hosts through SSH.
- Be very careful with all text printed in the SSH UI. Do *not* change the UI behavior without confirming the change with a human. In general, the service is very sparing with text shown to the user over SSH; adding more ruins the vibe.
- We have three ways of configuring container hosts: in prod (named exe-ctr-NN in Tailscale), locally for macOS dev (lima-exe-ctr and lima-exe-ctr-tests), and in CI (cloud-hypervisor on metal Ubuntu). All the scripts for configuring these are in ops/.
- If running inside an exe.dev VM (i.e. /exe.dev exists), you can test the full exed+exelet stack locally with:
  ```
  make exelet
  go build -o /tmp/exed-local ./cmd/exed/
  /tmp/exed-local -stage=local -start-exelet -db tmp
  ```
  Build exelet first to avoid OOM from concurrent Go compilations on small VMs. The exelet binary is cached at /tmp/exeletd after the first build.
- When editing Go code, run gofumpt on changed files. Do not run formatters on generated files.
- When editing code, run the tests for the relevant code.
- When editing OAuth, browser-facing, or multi-account flows: always manually test with real accounts and a real browser (using the browser tool) in addition to e2e tests. Mock servers cannot reproduce all real-world behaviors (account switchers, consent screens, token scoping). See `devdocs/for-agents/testing-integrations.md` for test accounts and steps.
- GitHub integration testing requires secrets in `~/.envrc-github` (not checked into git). This file contains the dev GitHub App credentials (`EXE_GITHUB_APP_*` env vars) and test user passwords (`SKETCHDEVTESTUSER_PASSWORD`, `SKETCHDEVTESTUSER2_PASSWORD`). To start exed with GitHub support: `source ~/.envrc-github && /tmp/exed-local -stage=local -start-exelet -db tmp`. See `devdocs/for-agents/testing-integrations.md` for full instructions.
- This is a production service; do not leave comments about "for production, do this..."; finish the job.
- Do not overly worry about compatibility; do not create shims to handle compatibility.
- NEVER create defaults for things that are required. If data is missing, either fix the missing data or fail with a clear error explaining what's wrong.
- Use `go test -count=1 ./e1e` to run end-to-end tests against a local container host. For faster results, run a specific test by name.
- Run `make lint` as a final sanity check after you are done making changes to Go code, to prevent "works locally but rejected by CI."
- Prefer sync.Mutex over sync.RWMutex unless there's a clear performance benefit from read-heavy workloads.
- Don't add sleeps in tests; instead, add retry loops with a very small sleep.
- Test everything end-to-end. Actually start containers and do things with them. Actually GET and POST against the server.
- Before fixing a bug, write a complete test that fails, then fix the bug (and thus the test).
- If you have a failing test sometimes, try something like `-count=1000 -failfast -run=ThatSpecificTest`.
- Use await syntax instead of .then()/.catch() where possible.
- Use sqlc to manage queries. Avoid writing test-only queries. It is OK to use 'select *' in queries; sqlc will expand it out to an explicit list of fields. Use withRxRes0/withRxRes1/withTx0/withTx1 to execute queries when possible.
- When a test is hard to set up, think hard about a small API change or a new primitive/concept that reshapes the API so the test is easy to write and easy to follow.
- For logging practices, see devdocs/logging.md.
- If you hit a permissions error, ask for more permissions, rather than working around it.
- When updating domains and links, use the env package, which sets the correct domains.
- Shell scripts should be concise with their output. `set -e` is more or less enough verbosity for finding when things are wrong. Traps like
    `trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR`
  help too.
- There are end-to-end agent-driven tests in e2e/. See there for details.
- Do not create documentation files (e.g., `.md`, `.txt`) unless specifically asked to do so. Agents should only reference docs that already exist in the repo—never invent paths to nonexistent files.

For web pages:
- Separate HTML templates, CSS, and JS into their own files.
- Try to reuse JS and CSS as much as reasonable.
- Web pages should be responsive and look good on both mobile and desktop.
