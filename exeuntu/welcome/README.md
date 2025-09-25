# Welcome Server

This example web server demonstrates end-to-end usage of exe.dev:

- Login/logout behind the exed proxy using its auth URLs
- Showing who is logged in (when proxy headers are present)
- Tracking per-user page views in SQLite with a simple migration

If you start an exe.dev welcome image, this web server will be in `/app`, and
will have been started by systemd. Disable it with `systemctl stop webserver`
and `systemctl disable webserver`.

Auth flow

- When not logged in, the page shows a Login link to:
  `https://{main-domain}/auth?redirect={path}&return_host={this-host}`
  This sends the user to the main domain to authenticate, then returns them to
  `https://{this-host}` and sets the subdomain cookie.
- When logged in, the page shows a Logout link to:
  `https://{this-host}/__exe.dev/logout`

Identity headers

- When proxied through exed, requests may include `X-Exedev-Userid` and
  `X-Exedev-Email`. If present, the server shows who you are and uses that to
  key the view counter.
- If headers are absent but the subdomain auth cookie exists, the page will
  show you as logged in but without identity details.

SQLite storage

- The server stores per-visitor counts in SQLite.
- Configure path via `WELCOME_DB_PATH` (default `./welcome.sqlite3`).
- The server auto-creates a `visitors` table and upserts counts per request.

Code layout

- `exeuntu/welcome/cmd/welcomed`: main package (binary entrypoint)
- `exeuntu/welcome/srv`: HTTP server logic (handlers)
- `exeuntu/welcome/db`: SQLite open + migrations (001-base.sql) like exed
- `exeuntu/welcome/sqlc`: optional sqlc schema/queries for codegen

Local dev

- Build: `go build -o webserver ./exeuntu/welcome/cmd/welcomed`
- Run: `WELCOME_DB_PATH=./welcome.sqlite3 ./webserver`
