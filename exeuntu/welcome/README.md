# Welcome Server

This example web server demonstrates end-to-end usage of exe.dev.

## Starting and Stopping the server

The welcome server runs as a systemd unit called "welcome-exedev-webapp".
You can see logs with `sudo journalctl -u welcome-exedev-webapp`. Build
with `make build` and deploy with `make restart`.

## Authorization

exe.dev provides authorization headers and login/logout links
that this repostitory uses.

When proxied through exed, requests will include `X-ExeDev-UserID` and
`X-ExeDev-Email` if the user is authenticated via exe.dev.

## Database

This app uses sqlite. SQL queries are managed with
sqlc.

## Code layout

- `cmd/welcomed`: main package (binary entrypoint)
- `srv`: HTTP server logic (handlers)
- `srv/templates`: Go templates the web server
- `db`: SQLite open + migrations (001-base.sql)
- `sqlc`: optional sqlc schema/queries for codegen
