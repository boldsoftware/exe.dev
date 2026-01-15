- exe.dev is a service users can use to start containers with persistent disks, read README.md and ARCHITECTURE.md for more. the ./cmd/exed binary implements the web and ssh frontend and acts as container controller, instructing ctr-hosts through ssh.
- be very careful with all text printed in the SSH UI. do *not* change the UI behavior without confirming the change with a human. in general, the service is very sparing with text shown to the user over ssh, adding more ruins the vibe.
- we have three ways of configuring container hosts: in prod (named exe-ctr-NN in tailscale), locally for macOS dev (lima-exe-ctr and lima-exe-ctr-tests), and in CI (libvirt on metal ubuntu). all the scripts for configuring these are in ops/
- when editing Go code, run gofumpt on changed files. do not run formatters on generated files.
- this is a production service; do not leave comments about "for production, do this..."; finish the job
- do not overly worry about compatibility; do not create shims to handle compatibility
- NEVER create defaults for things that are required. If data is missing, either fix the missing data or fail with a clear error explaining what's wrong
- use `go test -count=1 ./e1e` to run end-to-end tests against a local container host. for faster results, run a specific test by name.
- run `make lint` as a final sanity check after you are done making changes to Go code, to prevent "works locally but rejected by CI"
- prefer sync.Mutex over sync.RWMutex unless there's a clear performance benefit from read-heavy workloads
- don't add sleeps in tests; instead, add retry loops with a very small sleep
- use await syntax instead of .then()/.catch() where possible
- use sqlc to manage queries. avoid writing test-only queries. it is OK to use 'select *' in queries; sqlc will expand it out to an explicit list of fields. use withRxRes0/withRxRes1/withTx0/withTx1 to execute queries when possible.
- if you hit a permissions error, ask for more permissions, rather than working around it.
- shell scripts should be concise with their output; set -e is more or less enough verbosity for finding when
  things are wrong; traps like
    trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR
  help too

For web pages:
- Separate HTML templates, CSS, and JS into their own files.
- Try to re-use JS and CSS as much as reasonable.
- Web pages should be responsive and look good on both mobile and desktop.
