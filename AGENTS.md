- exe.dev is a service users can use to start containers with persistent disks, read README.md and ARCHITECTURE.md for more. the ./cmd/exed binary implements the web and ssh frontend and acts as container controller, instructing ctr-hosts through ssh.
- be very careful with all text printed in the SSH UI. do *not* change the UI behavior without confirming the change with a human. in general, the service is very sparing with text shown to the user over ssh, adding more ruins the vibe.
- we have three ways of configuring container hosts: in prod (named exe-ctr-NN in tailscale), locally for macOS dev (lima-exe-ctr and lima-exe-ctr-tests), and in CI (libvirt on metal ubuntu). all the scripts for configuring these are in ops/
- if running inside an exe.dev VM (i.e. /exe.dev exists), you can test the full exed+exelet stack locally with:
  ```
  make exelet
  go build -o /tmp/exed-local ./cmd/exed/
  /tmp/exed-local -stage=local -start-exelet -db tmp
  ```
  build exelet first to avoid OOM from concurrent Go compilations on small VMs. the exelet binary is cached at /tmp/exeletd after the first build.
- when editing Go code, run gofumpt on changed files. do not run formatters on generated files.
- when editing code, run the tests for the relevant code.
- this is a production service; do not leave comments about "for production, do this..."; finish the job
- do not overly worry about compatibility; do not create shims to handle compatibility
- NEVER create defaults for things that are required. If data is missing, either fix the missing data or fail with a clear error explaining what's wrong
- use `go test -count=1 ./e1e` to run end-to-end tests against a local container host. for faster results, run a specific test by name.
- run `make lint` as a final sanity check after you are done making changes to Go code, to prevent "works locally but rejected by CI"
- prefer sync.Mutex over sync.RWMutex unless there's a clear performance benefit from read-heavy workloads
- don't add sleeps in tests; instead, add retry loops with a very small sleep
- test everything end-to-end. actually start containers and do things with them. actually GET and POST against the server.
- before fixing a bug, write a complete test that fails, then fix the bug (and thus the test).
- if you have a failing test sometimes, try something like `-count=1000 -failfast -run=ThatSpecificTest`.
- use await syntax instead of .then()/.catch() where possible
- use sqlc to manage queries. avoid writing test-only queries. it is OK to use 'select *' in queries; sqlc will expand it out to an explicit list of fields. use withRxRes0/withRxRes1/withTx0/withTx1 to execute queries when possible.
- NEVER import `exe.dev/exedb` in new code. Use package-level APIs/helpers instead.
- NEVER inline SQL in tests.
- When a test is hard to set up, think hard about a small API change or a new primitive/concept that reshapes the API so the test is easy to write and easy to follow.
- for logging practices, see devdocs/logging.md
- if you hit a permissions error, ask for more permissions, rather than working around it.
- When updating domains and links, use the env package, which sets the correct domains.
- shell scripts should be concise with their output; set -e is more or less enough verbosity for finding when
  things are wrong; traps like
    trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR
  help too
- there are end-to-end agent-driven tests in e2e/. See there for details.
- do not create documentation files (e.g., `.md`, `.txt`) unless specifically asked to do so. Agents should only reference docs that already exist in the repo — never invent paths to nonexistent files.
For web pages:
- Separate HTML templates, CSS, and JS into their own files.
- Try to re-use JS and CSS as much as reasonable.
- Web pages should be responsive and look good on both mobile and desktop.
