- exe.dev provides cloud VMs.
- The devdocs dir contains extra documentation.
- Be very sparing with text printed in the SSH UI. Treat UX changes in the lobby as public APIs: tread carefully.
- If running inside an exe.dev VM (i.e. /exe.dev exists), you can test the full exed+exelet stack locally with:
  ```
  make exelet
  go build -o /tmp/exed-local ./cmd/exed/
  /tmp/exed-local -stage=local -start-exelet -db tmp
  ```
  Build exelet first to avoid OOM from concurrent Go compilations on small VMs. The exelet binary is cached at /tmp/exeletd after the first build.
- Use gofumpt and goimports -w. Do not run formatters on generated files.
- When editing OAuth, browser-facing, or multi-account flows, manually test with real accounts and a real browser (using the browser tool) in addition to e1e tests. Mock servers cannot reproduce all real-world behaviors (account switchers, consent screens, token scoping). See `devdocs/for-agents/testing-integrations.md` for test accounts and steps.
- Use `go test -count=1 ./e1e` (not a typo: the directory is named `e1e`) to run end-to-end tests against a local container host. For much faster results, run a specific test by name.
- Don't add sleeps in tests. Best is to use testing/synctest. Failing that, add retry loops with a very small sleep.
- Test everything end-to-end. No shortcuts.
- Use red/green TDD.
- Use sqlc to manage queries. Avoid test-only queries; prefer to re-use non-test APIs/helpers. It is OK to use 'select *' in queries; sqlc will expand it out to an explicit list of fields. Use withRxRes0/withRxRes1/withTx0/withTx1 when possible.
- For logging practices, see devdocs/logging.md.
- If you hit a permissions error, ask for more permissions, rather than working around it.
- When generating domains and/or links, use package stage. Pass around stage.Envs in their entirety, rather than individual fields.
- Shell scripts should be concise or even silent on success. Follow the unix philosophy. `set -e` is enough verbosity for debugging. Traps like `trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR` help too.
- Do not create new documentation files unless specifically asked to do so.
- Separate HTML templates, CSS, and JS into their own files.
- Try to reuse JS and CSS as much as reasonable.
- Web pages should be responsive and look good on both mobile and desktop.
